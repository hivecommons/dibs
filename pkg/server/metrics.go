package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// metricsLogInterval is how often accumulated request metrics are flushed
// to the log and reset. Kept coarse (unlike healthz/readyz, which must be
// cheap and instant) since this only drives periodic visibility, not a
// live query path.
const metricsLogInterval = 5 * time.Minute

// apiRouteSegments is the closed set of first-path-segment labels under
// "/api/" that dibsAPI.Register (pkg/api) and server.New actually serve.
// Bounding the label to this allow-list (rather than echoing whatever an
// unauthenticated caller sent) keeps cardinality fixed even under a
// probing/scanning client hammering arbitrary "/api/<junk>" paths.
var apiRouteSegments = map[string]bool{
	"me":            true,
	"admin":         true,
	"intake":        true,
	"ideas":         true,
	"repos":         true,
	"refine":        true,
	"notifications": true,
	"credits":       true,
	"leaderboard":   true,
	"ticker":        true,
	"board":         true,
	"stats":         true,
}

// methodLabels is the closed set of HTTP methods recorded verbatim. Go's
// net/http accepts any RFC 7230 token as a method, so echoing r.Method
// directly would let an unauthenticated client mint a fresh label (and
// map entry, and flushed log line) per request. Anything else collapses
// into a single "OTHER" bucket.
var methodLabels = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodPost:    true,
	http.MethodPut:     true,
	http.MethodPatch:   true,
	http.MethodDelete:  true,
	http.MethodOptions: true,
}

// methodLabel bounds a request method to methodLabels or "OTHER".
func methodLabel(method string) string {
	if methodLabels[method] {
		return method
	}
	return "OTHER"
}

// routeGroup maps a request path to a small, fixed set of route labels.
// It never returns the raw path, an idea/repo ID, a token, or a query
// string — only method + this label + status class are ever recorded,
// matching the bounded dimensions the issue calls for.
func routeGroup(base, path string) string {
	p := strings.TrimPrefix(path, base)
	switch p {
	case "", "/":
		return "ui"
	case "/healthz":
		return "healthz"
	case "/readyz":
		return "readyz"
	case "/mcp":
		return "mcp"
	}
	if rest, ok := strings.CutPrefix(p, "/api/"); ok {
		seg := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			seg = rest[:i]
		}
		if apiRouteSegments[seg] {
			return "api/" + seg
		}
		return "api/other"
	}
	return "other"
}

// statusClass buckets an HTTP status into its "2xx"/"4xx"/... class —
// never the literal code, which would multiply cardinality for no
// operational benefit.
func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "xxx"
	}
	return fmt.Sprintf("%dxx", status/100)
}

// metricKey is the bounded label set a request is recorded under.
type metricKey struct {
	Method      string
	Route       string
	StatusClass string
}

// metricSample is one flushed (key, aggregate) pair, in a stable order for
// logging and tests.
type metricSample struct {
	Method      string
	Route       string
	StatusClass string
	Count       int64
	AvgMS       float64
}

// requestMetrics accumulates bounded HTTP request counts/durations. It
// never stores raw paths, idea/repo/user identifiers, or query strings —
// only the (method, route group, status class) key above.
type requestMetrics struct {
	mu     sync.Mutex
	counts map[metricKey]int64
	durMS  map[metricKey]int64
	// totals are cumulative since process start and never reset, so a
	// scraper sees monotonic counters; counts/durMS are the log window.
	totals map[metricKey]int64
	// hists are cumulative latency histograms keyed by the same bounded key.
	hists map[metricKey]*latencyHist
}

// latencyBuckets are fixed upper bounds in seconds; the bucket set never
// grows with traffic.
var latencyBuckets = [...]float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type latencyHist struct {
	buckets [len(latencyBuckets)]int64
	count   int64
	sum     float64
}

// reqStats is the process-wide request counter set read by MetricsHandler.
var reqStats = newRequestMetrics()

func newRequestMetrics() *requestMetrics {
	return &requestMetrics{counts: map[metricKey]int64{}, durMS: map[metricKey]int64{}, totals: map[metricKey]int64{}, hists: map[metricKey]*latencyHist{}}
}

func (m *requestMetrics) record(method, route string, status int, dur time.Duration) {
	key := metricKey{Method: method, Route: route, StatusClass: statusClass(status)}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[key]++
	m.totals[key]++
	m.durMS[key] += dur.Milliseconds()
	h := m.hists[key]
	if h == nil {
		h = &latencyHist{}
		m.hists[key] = h
	}
	secs := dur.Seconds()
	for i, ub := range latencyBuckets {
		if secs <= ub {
			h.buckets[i]++
		}
	}
	h.count++
	h.sum += secs
}

// writeProm writes the cumulative request totals in Prometheus text
// exposition format, sorted deterministically.
func (m *requestMetrics) writeProm(w io.Writer) {
	m.mu.Lock()
	keys := make([]metricKey, 0, len(m.totals))
	vals := make(map[metricKey]int64, len(m.totals))
	for k, n := range m.totals {
		keys = append(keys, k)
		vals[k] = n
	}
	hists := make(map[metricKey]latencyHist, len(m.hists))
	for k, h := range m.hists {
		hists[k] = *h
	}
	m.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Route != b.Route {
			return a.Route < b.Route
		}
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		return a.StatusClass < b.StatusClass
	})
	fmt.Fprintln(w, "# HELP dibs_http_requests_total HTTP requests by method, route group and status class.")
	fmt.Fprintln(w, "# TYPE dibs_http_requests_total counter")
	for _, k := range keys {
		fmt.Fprintf(w, "dibs_http_requests_total{method=%q,route_group=%q,status_class=%q} %d\n", k.Method, k.Route, k.StatusClass, vals[k])
	}
	fmt.Fprintln(w, "# HELP dibs_http_request_duration_seconds HTTP request latency by method, route group and status class.")
	fmt.Fprintln(w, "# TYPE dibs_http_request_duration_seconds histogram")
	for _, k := range keys {
		h := hists[k]
		lbl := fmt.Sprintf("method=%q,route_group=%q,status_class=%q", k.Method, k.Route, k.StatusClass)
		for i, ub := range latencyBuckets {
			fmt.Fprintf(w, "dibs_http_request_duration_seconds_bucket{%s,le=%q} %d\n", lbl, strconv.FormatFloat(ub, 'g', -1, 64), h.buckets[i])
		}
		fmt.Fprintf(w, "dibs_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", lbl, h.count)
		fmt.Fprintf(w, "dibs_http_request_duration_seconds_sum{%s} %g\n", lbl, h.sum)
		fmt.Fprintf(w, "dibs_http_request_duration_seconds_count{%s} %d\n", lbl, h.count)
	}
}

// snapshotAndReset returns the accumulated samples sorted deterministically
// and clears the window counters (not the cumulative totals), so memory
// stays bounded to the number of distinct keys seen since the last flush
// (itself bounded by the fixed label sets above).
func (m *requestMetrics) snapshotAndReset() []metricSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.counts) == 0 {
		return nil
	}
	samples := make([]metricSample, 0, len(m.counts))
	for key, count := range m.counts {
		samples = append(samples, metricSample{
			Method:      key.Method,
			Route:       key.Route,
			StatusClass: key.StatusClass,
			Count:       count,
			AvgMS:       float64(m.durMS[key]) / float64(count),
		})
	}
	m.counts = map[metricKey]int64{}
	m.durMS = map[metricKey]int64{}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Route != samples[j].Route {
			return samples[i].Route < samples[j].Route
		}
		if samples[i].Method != samples[j].Method {
			return samples[i].Method < samples[j].Method
		}
		return samples[i].StatusClass < samples[j].StatusClass
	})
	return samples
}

// logSnapshot flushes accumulated request metrics to the log and resets them.
func (m *requestMetrics) logSnapshot() {
	for _, s := range m.snapshotAndReset() {
		slog.Info("metrics http", "method", s.Method, "route", s.Route, "status", s.StatusClass, "count", s.Count, "avg_ms", s.AvgMS)
	}
}

// logPeriodically flushes accumulated metrics on a fixed interval. The
// cumulative totals are separately exposed on the internal /metrics
// listener (see MetricsHandler); no exporter or metrics SDK dependency is
// involved.
func (m *requestMetrics) logPeriodically(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		m.logSnapshot()
	}
}

// statusRecorder captures the status code a handler wrote, defaulting to
// 200 if the handler never called WriteHeader explicitly (the standard
// net/http convention).
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.written {
		w.status = http.StatusOK
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.NewResponseController reach the underlying writer so
// Flush/Hijack/SetWriteDeadline keep working through this wrapper. The MCP
// streamable-HTTP handler flushes every SSE event via ResponseController;
// without Unwrap those flushes fail with ErrNotSupported and events sit
// buffered until the handler returns.
func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// wrap instruments every request through next — public, authenticated,
// rejected, and MCP alike — with bounded (method, route group, status
// class) counts and durations. It sits outermost in New so it also
// accounts for csrfGuard/auth rejections, not just the routes that reach
// a final handler.
func (m *requestMetrics) wrap(base string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		// Deferred so a panicking handler (recovered by net/http) is still
		// counted, as a 5xx, instead of silently vanishing from the SLO data.
		defer func() {
			status := rec.status
			if p := recover(); p != nil {
				if !rec.written {
					status = http.StatusInternalServerError
				}
				m.record(methodLabel(r.Method), routeGroup(base, r.URL.Path), status, time.Since(start))
				panic(p)
			}
			if !rec.written {
				status = http.StatusOK
			}
			m.record(methodLabel(r.Method), routeGroup(base, r.URL.Path), status, time.Since(start))
		}()
		next.ServeHTTP(rec, r)
	})
}
