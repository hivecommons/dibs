package server

import (
	"testing"
	"time"
)

// TestSnapshotOrdersByStatusClassWithinRoute pins the full sort key of the
// flushed samples: route, then method, then status class. Rows that share a
// route and method must still come out in a stable order so log lines diff
// cleanly between flushes.
func TestSnapshotOrdersByStatusClassWithinRoute(t *testing.T) {
	m := newRequestMetrics()
	m.record("GET", "api/ideas", 500, time.Millisecond)
	m.record("GET", "api/ideas", 404, time.Millisecond)
	m.record("GET", "api/ideas", 200, time.Millisecond)
	m.record("DELETE", "api/ideas", 204, time.Millisecond)
	m.record("GET", "api/admin", 200, time.Millisecond)

	got := m.snapshotAndReset()
	want := []struct{ Method, Route, StatusClass string }{
		{"GET", "api/admin", "2xx"},
		{"DELETE", "api/ideas", "2xx"},
		{"GET", "api/ideas", "2xx"},
		{"GET", "api/ideas", "4xx"},
		{"GET", "api/ideas", "5xx"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Method != w.Method || g.Route != w.Route || g.StatusClass != w.StatusClass {
			t.Errorf("samples[%d] = %s %s %s, want %s %s %s", i, g.Method, g.Route, g.StatusClass, w.Method, w.Route, w.StatusClass)
		}
	}
}
