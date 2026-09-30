package server

import (
	"fmt"
	"log"
	"net/http"
	"sort"
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
}

func newRequestMetrics() *requestMetrics {
	return &requestMetrics{counts: map[metricKey]int64{}, durMS: map[metricKey]int64{}}
}

func (m *requestMetrics) record(method, route string, status int, dur time.Duration) {
	key := metricKey{Method: method, Route: route, StatusClass: statusClass(status)}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[key]++
	m.durMS[key] += dur.Milliseconds()
}

// snapshotAndReset returns the accumulated samples sorted deterministically
// and clears the counters, so memory stays bounded to the number of
// distinct keys seen since the last flush (itself bounded by the fixed
// label sets above).
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

// logPeriodically flushes accumulated metrics on a fixed interval. This is
// intentionally log-only: no external exporter, scrape endpoint, or
// metrics SDK dependency is added until a backend is chosen (see issue
// #184) — it just gives operators the bounded request visibility the
// current /healthz and /readyz probes cannot.
func (m *requestMetrics) logPeriodically(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		for _, s := range m.snapshotAndReset() {
			log.Printf("metrics: method=%s route=%s status=%s count=%d avg_ms=%.1f", s.Method, s.Route, s.StatusClass, s.Count, s.AvgMS)
		}
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

// wrap instruments every request through next — public, authenticated,
// rejected, and MCP alike — with bounded (method, route group, status
// class) counts and durations. It sits outermost in New so it also
// accounts for csrfGuard/auth rejections, not just the routes that reach
// a final handler.
func (m *requestMetrics) wrap(base string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		status := rec.status
		if !rec.written {
			status = http.StatusOK
		}
		m.record(r.Method, routeGroup(base, r.URL.Path), status, time.Since(start))
	})
}
