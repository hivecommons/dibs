package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRouteGroupBounded(t *testing.T) {
	cases := []struct {
		base, path, want string
	}{
		{"", "/", "ui"},
		{"", "/healthz", "healthz"},
		{"", "/readyz", "readyz"},
		{"", "/mcp", "mcp"},
		{"", "/api/credits", "api/credits"},
		{"", "/api/ideas", "api/ideas"},
		{"", "/api/ideas/abc-123", "api/ideas"},
		{"", "/api/repos/org/repo/index", "api/repos"},
		// Unknown/attacker-controlled segments must collapse to a single
		// bounded bucket instead of creating a new label per input.
		{"", "/api/does-not-exist", "api/other"},
		{"", "/api/", "api/other"},
		{"", "/no-such-route", "other"},
		// Base-path prefix (DIBS_BASE_PATH) must still resolve correctly.
		{"/ideas", "/ideas/healthz", "healthz"},
		{"/ideas", "/ideas/api/board", "api/board"},
	}
	for _, c := range cases {
		if got := routeGroup(c.base, c.path); got != c.want {
			t.Errorf("routeGroup(%q, %q) = %q, want %q", c.base, c.path, got, c.want)
		}
	}
}

func TestStatusClass(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{200, "2xx"},
		{201, "2xx"},
		{301, "3xx"},
		{404, "4xx"},
		{500, "5xx"},
		{0, "xxx"},
	}
	for _, c := range cases {
		if got := statusClass(c.status); got != c.want {
			t.Errorf("statusClass(%d) = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestRequestMetricsRecordAndSnapshot(t *testing.T) {
	m := newRequestMetrics()
	if got := m.snapshotAndReset(); got != nil {
		t.Fatalf("snapshotAndReset on empty metrics = %v, want nil", got)
	}
	m.record("GET", "api/ideas", 200, 10*time.Millisecond)
	m.record("GET", "api/ideas", 200, 30*time.Millisecond)
	m.record("GET", "healthz", 200, time.Millisecond)
	m.record("POST", "api/ideas", 500, 5*time.Millisecond)

	samples := m.snapshotAndReset()
	if len(samples) != 3 {
		t.Fatalf("got %d samples, want 3: %+v", len(samples), samples)
	}
	var found bool
	for _, s := range samples {
		if s.Method == "GET" && s.Route == "api/ideas" && s.StatusClass == "2xx" {
			found = true
			if s.Count != 2 {
				t.Errorf("count = %d, want 2", s.Count)
			}
			if s.AvgMS != 20 {
				t.Errorf("avg_ms = %v, want 20", s.AvgMS)
			}
		}
	}
	if !found {
		t.Fatalf("expected a GET api/ideas 2xx sample, got %+v", samples)
	}

	// Recording resets the accumulators: memory does not grow unbounded
	// across flushes.
	if got := m.snapshotAndReset(); got != nil {
		t.Fatalf("snapshotAndReset after flush = %v, want nil", got)
	}
}

func TestRequestMetricsWrap(t *testing.T) {
	m := newRequestMetrics()
	inner := http.NewServeMux()
	inner.HandleFunc("GET /api/ideas", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	inner.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		// No explicit WriteHeader: must default to 200.
		_, _ = w.Write([]byte("ok"))
	})

	wrapped := m.wrap("", inner)

	req := httptest.NewRequest(http.MethodGet, "/api/ideas", nil)
	wrapped.ServeHTTP(httptest.NewRecorder(), req)

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	wrapped.ServeHTTP(httptest.NewRecorder(), req)

	// A rejected/unmatched route (404) must still be recorded, not dropped.
	req = httptest.NewRequest(http.MethodGet, "/no-such-route", nil)
	wrapped.ServeHTTP(httptest.NewRecorder(), req)

	samples := m.snapshotAndReset()
	if len(samples) != 3 {
		t.Fatalf("got %d samples, want 3: %+v", len(samples), samples)
	}
	want := map[metricKey]bool{
		{Method: "GET", Route: "api/ideas", StatusClass: "2xx"}: true,
		{Method: "GET", Route: "healthz", StatusClass: "2xx"}:   true,
		{Method: "GET", Route: "other", StatusClass: "4xx"}:     true,
	}
	for _, s := range samples {
		key := metricKey{Method: s.Method, Route: s.Route, StatusClass: s.StatusClass}
		if !want[key] {
			t.Errorf("unexpected sample %+v", s)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Errorf("missing samples: %+v", want)
	}
}

func TestMethodLabelBounded(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if got := methodLabel(m); got != m {
			t.Errorf("methodLabel(%q) = %q, want verbatim", m, got)
		}
	}
	for _, m := range []string{"PROPFIND", "get", "FOO-123", ""} {
		if got := methodLabel(m); got != "OTHER" {
			t.Errorf("methodLabel(%q) = %q, want OTHER", m, got)
		}
	}
}

func TestWrapUnknownMethodsCollapse(t *testing.T) {
	m := newRequestMetrics()
	wrapped := m.wrap("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	// Go's net/http accepts any token as a method; a probing client must
	// not be able to mint one metric key per request.
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(fmt.Sprintf("M%03d", i), "/healthz", nil)
		wrapped.ServeHTTP(httptest.NewRecorder(), req)
	}
	samples := m.snapshotAndReset()
	if len(samples) != 1 {
		t.Fatalf("got %d samples, want 1: %+v", len(samples), samples)
	}
	if samples[0].Method != "OTHER" || samples[0].Count != 100 {
		t.Errorf("sample = %+v, want Method=OTHER Count=100", samples[0])
	}
}

func TestStatusRecorderUnwrapPreservesFlush(t *testing.T) {
	m := newRequestMetrics()
	var flushErr error
	wrapped := m.wrap("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The MCP streamable-HTTP handler flushes every SSE event this way;
		// it must reach the real writer through the metrics wrapper.
		flushErr = http.NewResponseController(w).Flush()
	}))
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if flushErr != nil {
		t.Fatalf("Flush through metrics wrapper: %v", flushErr)
	}
	if !rr.Flushed {
		t.Fatal("underlying recorder was not flushed")
	}
}

func TestWrapRecordsPanickingHandlerAs5xx(t *testing.T) {
	m := newRequestMetrics()
	h := m.wrap("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic to propagate")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/me", nil))
	}()
	got := m.snapshotAndReset()
	if len(got) != 1 || got[0].StatusClass != "5xx" || got[0].Count != 1 {
		t.Fatalf("unexpected samples: %+v", got)
	}
}

func TestRequestMetricsLogSnapshot(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	m := newRequestMetrics()
	m.record("GET", "api/ideas", 200, 10*time.Millisecond)
	m.logSnapshot()
	want := `"msg":"metrics http","method":"GET","route":"api/ideas","status":"2xx","count":1,"avg_ms":10`
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("log missing %s:\n%s", want, buf.String())
	}
	if got := m.snapshotAndReset(); got != nil {
		t.Fatalf("logSnapshot must reset, got %v", got)
	}
}

func TestRequestMetricsTotalsMonotonic(t *testing.T) {
	m := newRequestMetrics()
	m.record("GET", "api/ideas", 200, time.Millisecond)
	m.snapshotAndReset()
	m.record("GET", "api/ideas", 200, time.Millisecond)
	m.record("GET", "readyz", 503, time.Millisecond)

	var buf bytes.Buffer
	m.writeProm(&buf)
	out := buf.String()
	for _, want := range []string{
		"# TYPE dibs_http_requests_total counter\n",
		`dibs_http_requests_total{method="GET",route_group="api/ideas",status_class="2xx"} 2` + "\n",
		`dibs_http_requests_total{method="GET",route_group="readyz",status_class="5xx"} 1` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestMetricsHandler(t *testing.T) {
	reqStats.record("POST", "api/ideas", 201, time.Millisecond)
	RecordJob(JobRegistrySync, nil)
	rr := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	out := rr.Body.String()
	for _, want := range []string{
		"dibs_http_requests_total{",
		`dibs_background_job_runs_total{job="registry_sync",result="ok"}`,
		"# TYPE dibs_background_job_last_success_timestamp_seconds gauge",
		"# TYPE dibs_match_llm_calls_total counter",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	rr = httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("/other status = %d, want 404", rr.Code)
	}
}
