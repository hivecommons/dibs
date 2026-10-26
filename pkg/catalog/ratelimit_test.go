package catalog

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

const rateLimitLandscapeYAML = `
landscape:
  - name: Orchestration
    subcategories:
      - name: Scheduling
        items:
          - name: alpha
            repo_url: https://github.com/cncf/alpha
            project: graduated
          - name: beta
            repo_url: https://github.com/cncf/beta
            project: sandbox
          - name: gamma
            repo_url: https://github.com/cncf/gamma
            project: sandbox
`

func TestRefreshStopsEnrichingWhenRateLimited(t *testing.T) {
	var reqs atomic.Int32
	reset := time.Now().Add(time.Hour)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer gh.Close()

	s := newRefreshStore(t, gh, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(rateLimitLandscapeYAML))
	})
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := reqs.Load(); got != 1 {
		t.Fatalf("GitHub requests = %d, want 1", got)
	}
	if got := len(s.List()); got != 3 {
		t.Fatalf("persisted projects = %d, want 3", got)
	}

	if err := s.Refresh(t.Context()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if got := reqs.Load(); got != 1 {
		t.Fatalf("GitHub requests before reset = %d, want 1", got)
	}
	if got := len(s.List()); got != 3 {
		t.Fatalf("projects after second refresh = %d, want 3", got)
	}
}
