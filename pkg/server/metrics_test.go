package server

import (
	"net/http"
	"net/http/httptest"
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
