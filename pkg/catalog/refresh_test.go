package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const refreshLandscapeYAML = `
landscape:
  - name: Orchestration
    subcategories:
      - name: Scheduling
        items:
          - name: alpha
            description: landscape says alpha
            repo_url: https://github.com/cncf/alpha
            project: graduated
          - name: beta
            description: landscape says beta
            repo_url: https://github.com/cncf/beta
            project: sandbox
`

// landscapeTransport routes the hardcoded LandscapeURL to an in-test
// handler and forwards everything else (the GitHub API base) unchanged.
type landscapeTransport struct {
	landscape http.HandlerFunc
}

func (t *landscapeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() == LandscapeURL {
		rec := httptest.NewRecorder()
		t.landscape(rec, req)
		return rec.Result(), nil
	}
	return http.DefaultTransport.RoundTrip(req)
}

// newRefreshStore wires a Store whose landscape fetch is served by
// landscape and whose GitHub API base is the given httptest server.
func newRefreshStore(t *testing.T, github *httptest.Server, landscape http.HandlerFunc) *Store {
	t.Helper()
	s, err := New(t.TempDir(), "test-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.BaseURL = github.URL
	s.Client = &http.Client{Transport: &landscapeTransport{landscape: landscape}, Timeout: requestTimeout}
	s.Logf = t.Logf
	return s
}

func serveLandscape(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(refreshLandscapeYAML))
}

func TestRefreshEnrichesAndPersists(t *testing.T) {
	var sawAuth, sawAPIVersion string
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawAPIVersion = r.Header.Get("X-GitHub-Api-Version")
		switch r.URL.Path {
		case "/repos/cncf/alpha":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"description": "  enriched alpha  ",
				"topics":      []string{"mesh", "proxy"},
				"language":    " Go ",
			})
		case "/repos/cncf/alpha/readme":
			_, _ = w.Write([]byte("# Alpha\n\nAlpha does service mesh things.\n"))
		case "/repos/cncf/beta":
			w.WriteHeader(http.StatusNotFound) // enrichment skipped, landscape data kept
		default:
			t.Errorf("unexpected GitHub path %s", r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer github.Close()

	s := newRefreshStore(t, github, serveLandscape)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }

	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if sawAuth != "Bearer test-token" {
		t.Fatalf("Authorization header %q, want bearer token", sawAuth)
	}
	if sawAPIVersion != "2022-11-28" {
		t.Fatalf("X-GitHub-Api-Version %q", sawAPIVersion)
	}

	projects := s.List()
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	alpha, beta := projects[0], projects[1]
	if alpha.RepoID != "cncf/alpha" || beta.RepoID != "cncf/beta" {
		t.Fatalf("unexpected order: %s, %s", alpha.RepoID, beta.RepoID)
	}
	if alpha.Description != "enriched alpha" {
		t.Errorf("alpha description %q, want trimmed GitHub description", alpha.Description)
	}
	if alpha.Language != "Go" || len(alpha.Topics) != 2 {
		t.Errorf("alpha language %q topics %v", alpha.Language, alpha.Topics)
	}
	if !strings.Contains(alpha.Readme, "Alpha does service mesh things.") {
		t.Errorf("alpha readme %q missing intro line", alpha.Readme)
	}
	if strings.Contains(alpha.Readme, "# Alpha") {
		t.Errorf("alpha readme %q kept a heading", alpha.Readme)
	}
	if beta.Description != "landscape says beta" {
		t.Errorf("beta description %q, want landscape fallback after 404", beta.Description)
	}
	if !alpha.FetchedAt.Equal(now) {
		t.Errorf("alpha FetchedAt %v, want %v", alpha.FetchedAt, now)
	}

	// The refreshed catalog is queryable immediately...
	if got := s.TopK("service mesh", 5); len(got) == 0 || got[0].Project.RepoID != "cncf/alpha" {
		t.Fatalf("TopK after refresh = %+v, want alpha first", got)
	}
	// ...and was persisted so a fresh Store rehydrates from disk.
	reloaded, err := New(filepath.Dir(s.path), "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reloaded.List(); len(got) != 2 || got[0].Readme != alpha.Readme {
		t.Fatalf("reloaded catalog does not match persisted refresh")
	}
}

func TestRefreshLandscapeErrorLeavesCatalogUntouched(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("GitHub must not be called when the landscape fetch fails")
	}))
	defer github.Close()

	s := newRefreshStore(t, github, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	s.setProjects([]Project{{Name: "keep", RepoID: "cncf/keep"}})

	err := s.Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("Refresh error = %v, want landscape status error", err)
	}
	if got := s.List(); len(got) != 1 || got[0].RepoID != "cncf/keep" {
		t.Fatalf("catalog mutated on failed refresh: %+v", got)
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatalf("cache file written on failed refresh (stat err %v)", err)
	}
}

func TestRefreshSurvivesEnrichServerError(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // hard error: logged, project kept bare
	}))
	defer github.Close()

	var mu sync.Mutex
	var logged []string
	s := newRefreshStore(t, github, serveLandscape)
	s.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, format)
	}

	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v (enrich errors must not abort the refresh)", err)
	}
	projects := s.List()
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	if projects[0].Description != "landscape says alpha" {
		t.Errorf("alpha description %q, want landscape value kept", projects[0].Description)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 2 {
		t.Errorf("logged %d enrich failures, want 2", len(logged))
	}
}

func TestGetReadmeDecodesJSONContentFallback(t *testing.T) {
	intro := "Beta is a tiny sandbox project."
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/cncf/beta/readme":
			// A proxy that ignores the raw Accept header returns the JSON form.
			_ = json.NewEncoder(w).Encode(map[string]string{
				"content": base64.StdEncoding.EncodeToString([]byte("# Beta\n\n" + intro + "\n")),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	s := newRefreshStore(t, github, serveLandscape)
	got, err := s.getReadme(context.Background(), "cncf/beta")
	if err != nil {
		t.Fatalf("getReadme: %v", err)
	}
	if got != intro {
		t.Fatalf("getReadme = %q, want %q", got, intro)
	}
}

func TestGetJSONRateLimitAndErrorStatuses(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		wantErr string // "" means errSkip
	}{
		{name: "429 skips", status: http.StatusTooManyRequests},
		{name: "403 with Retry-After skips", status: http.StatusForbidden, headers: map[string]string{"Retry-After": "60"}},
		{name: "403 exhausted quota skips", status: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0"}},
		{name: "404 skips", status: http.StatusNotFound},
		{name: "plain 403 is an error", status: http.StatusForbidden, wantErr: "status 403"},
		{name: "500 is an error", status: http.StatusInternalServerError, wantErr: "status 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
			}))
			defer github.Close()

			s := newRefreshStore(t, github, serveLandscape)
			var out struct{}
			err := s.getJSON(context.Background(), "/repos/cncf/alpha", &out)
			if tc.wantErr == "" {
				if err != errSkip {
					t.Fatalf("err = %v, want errSkip", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestNeedsRefresh(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		projects []Project
		want     bool
	}{
		{name: "empty catalog", projects: nil, want: true},
		{name: "zero FetchedAt", projects: []Project{{RepoID: "a/a"}}, want: true},
		{name: "fresh", projects: []Project{{RepoID: "a/a", FetchedAt: now.Add(-time.Hour)}}, want: false},
		{name: "stale", projects: []Project{{RepoID: "a/a", FetchedAt: now.Add(-RefreshInterval)}}, want: true},
		{
			name: "oldest project wins",
			projects: []Project{
				{RepoID: "a/a", FetchedAt: now.Add(-time.Hour)},
				{RepoID: "b/b", FetchedAt: now.Add(-RefreshInterval - time.Hour)},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(t.TempDir(), "")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			s.Now = func() time.Time { return now }
			s.setProjects(tc.projects)
			if got := s.needsRefresh(); got != tc.want {
				t.Fatalf("needsRefresh = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRefreshAsyncRunsOnceAndClearsActive(t *testing.T) {
	release := make(chan struct{})
	calls := make(chan string, 8)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer github.Close()

	s := newRefreshStore(t, github, func(w http.ResponseWriter, r *http.Request) {
		calls <- r.URL.Path
		<-release
		_, _ = w.Write([]byte(refreshLandscapeYAML))
	})

	s.RefreshAsync()
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("first RefreshAsync never fetched the landscape")
	}
	// While the first refresh is in flight, further kicks are no-ops.
	s.RefreshAsync()
	select {
	case p := <-calls:
		t.Fatalf("second RefreshAsync started a concurrent fetch of %s", p)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	deadline := time.After(5 * time.Second)
	for {
		s.mu.RLock()
		done := !s.active && len(s.projects) == 2
		s.mu.RUnlock()
		if done {
			break
		}
		select {
		case <-deadline:
			t.Fatal("refresh never completed or active flag never cleared")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// A fresh catalog does not refresh again.
	s.RefreshAsync()
	select {
	case p := <-calls:
		t.Fatalf("RefreshAsync refetched a fresh catalog: %s", p)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRefreshAsyncNilStore(t *testing.T) {
	var s *Store
	s.RefreshAsync() // must not panic
}

func TestReadmeIntroStripsNoiseAndTruncates(t *testing.T) {
	in := strings.Join([]string{
		"# Heading",
		"[![badge](https://img)](https://link)",
		"<div align=center>",
		"---",
		"> quote line",
		"```go",
		"code := true",
		"```",
		"**Bold intro** stays",
		"A [link](https://example.com) survives as URL",
		"",
		"[ref]: https://refstyle",
	}, "\n")
	got := readmeIntro(in)
	want := "Bold intro** stays\nA https://example.com survives as URL"
	if got != want {
		t.Fatalf("readmeIntro = %q, want %q", got, want)
	}

	long := strings.Repeat("word ", 1000)
	if n := len([]rune(readmeIntro(long))); n > readmeIntroLimit {
		t.Fatalf("readmeIntro length %d exceeds limit %d", n, readmeIntroLimit)
	}

	if got := truncateRunes("héllo wörld", 5); got != "héllo" {
		t.Fatalf("truncateRunes = %q, want rune-safe cut", got)
	}
	if got := truncateRunes("short", 10); got != "short" {
		t.Fatalf("truncateRunes = %q, want unchanged", got)
	}
}
