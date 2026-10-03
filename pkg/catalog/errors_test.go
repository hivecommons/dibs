package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// errTransport fails every request with a fixed error.
type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// errBody is a response body whose first Read fails.
type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("body read failed") }
func (errBody) Close() error             { return nil }

// bodyErrTransport answers 200 OK with a body that cannot be read.
type bodyErrTransport struct{}

func (bodyErrTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: errBody{}, Request: req}, nil
}

func TestNewErrors(t *testing.T) {
	t.Run("data dir is a file", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(dir, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := New(dir, "")
		if err == nil || !strings.Contains(err.Error(), "creating data dir") {
			t.Fatalf("err = %v, want creating data dir error", err)
		}
	})
	t.Run("cache file is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, CacheFile), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := New(dir, "")
		if err == nil || !strings.Contains(err.Error(), "reading "+CacheFile) {
			t.Fatalf("err = %v, want reading %s error", err, CacheFile)
		}
	})
	t.Run("corrupt cache", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, CacheFile), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := New(dir, "")
		if err == nil || !strings.Contains(err.Error(), "corrupt "+CacheFile) {
			t.Fatalf("err = %v, want corrupt %s error", err, CacheFile)
		}
	})
	t.Run("missing cache is fine", func(t *testing.T) {
		s, err := New(t.TempDir(), "")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if got := s.List(); len(got) != 0 {
			t.Fatalf("List() = %v, want empty", got)
		}
		if got := s.TopK("anything", 3); len(got) != 0 {
			t.Fatalf("TopK on empty store = %v, want empty", got)
		}
	})
}

func TestGithubRequestDefaultsAndErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("default base and no token", func(t *testing.T) {
		s := &Store{}
		req, err := s.githubRequest(ctx, http.MethodGet, "/repos/o/r")
		if err != nil {
			t.Fatalf("githubRequest: %v", err)
		}
		if got := req.URL.String(); got != defaultGitHubAPI+"/repos/o/r" {
			t.Fatalf("URL = %q, want default GitHub API base", got)
		}
		if req.Header.Get("Authorization") != "" {
			t.Fatalf("Authorization = %q, want unset without a token", req.Header.Get("Authorization"))
		}
		if req.Header.Get("X-GitHub-Api-Version") == "" {
			t.Fatal("X-GitHub-Api-Version header missing")
		}
	})
	t.Run("trailing slash trimmed and token sent", func(t *testing.T) {
		s := &Store{BaseURL: "http://gh.test/api/", Token: "tok"}
		req, err := s.githubRequest(ctx, http.MethodGet, "/x")
		if err != nil {
			t.Fatalf("githubRequest: %v", err)
		}
		if got := req.URL.String(); got != "http://gh.test/api/x" {
			t.Fatalf("URL = %q, want single slash join", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization = %q, want Bearer tok", got)
		}
	})
	t.Run("invalid method", func(t *testing.T) {
		s := &Store{}
		if _, err := s.githubRequest(ctx, "BAD METHOD", "/x"); err == nil {
			t.Fatal("expected error for invalid method")
		}
	})
	t.Run("invalid base URL", func(t *testing.T) {
		s := &Store{BaseURL: "http://[::1"}
		if _, err := s.githubRequest(ctx, http.MethodGet, "/x"); err == nil {
			t.Fatal("expected error for unparsable base URL")
		}
	})
}

func TestClientDefaultsWhenUnset(t *testing.T) {
	s := &Store{}
	c := s.client()
	if c == nil || c.Timeout != requestTimeout {
		t.Fatalf("client() = %+v, want default client with %s timeout", c, requestTimeout)
	}
	custom := &http.Client{Timeout: time.Second}
	s.Client = custom
	if s.client() != custom {
		t.Fatal("client() did not return the configured client")
	}
}

func TestFetchLandscapeErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("transport error", func(t *testing.T) {
		boom := errors.New("boom")
		_, err := FetchLandscape(ctx, &http.Client{Transport: errTransport{boom}})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want wrapped boom", err)
		}
	})
	t.Run("non-200 status", func(t *testing.T) {
		client := &http.Client{Transport: &landscapeTransport{landscape: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}}}
		_, err := FetchLandscape(ctx, client)
		if err == nil || !strings.Contains(err.Error(), "landscape returned status 502") {
			t.Fatalf("err = %v, want status 502 error", err)
		}
	})
	t.Run("body read error", func(t *testing.T) {
		_, err := FetchLandscape(ctx, &http.Client{Transport: bodyErrTransport{}})
		if err == nil || !strings.Contains(err.Error(), "body read failed") {
			t.Fatalf("err = %v, want body read error", err)
		}
	})
}

func TestParseLandscapeInvalidYAML(t *testing.T) {
	_, err := ParseLandscape([]byte("landscape: [unterminated"))
	if err == nil || !strings.Contains(err.Error(), "parsing landscape") {
		t.Fatalf("err = %v, want parsing landscape error", err)
	}
}

func TestGetJSONAndReadmeTransportErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	t.Run("getJSON request build error", func(t *testing.T) {
		s := &Store{BaseURL: "http://[::1"}
		var out struct{}
		if err := s.getJSON(ctx, "/repos/o/r", &out); err == nil {
			t.Fatal("expected request build error")
		}
	})
	t.Run("getJSON transport error", func(t *testing.T) {
		s := &Store{BaseURL: "http://gh.test", Client: &http.Client{Transport: errTransport{boom}}}
		var out struct{}
		if err := s.getJSON(ctx, "/repos/o/r", &out); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want wrapped boom", err)
		}
	})
	t.Run("getReadme request build error", func(t *testing.T) {
		s := &Store{BaseURL: "http://[::1"}
		if _, err := s.getReadme(ctx, "o/r"); err == nil {
			t.Fatal("expected request build error")
		}
	})
	t.Run("getReadme transport error", func(t *testing.T) {
		s := &Store{BaseURL: "http://gh.test", Client: &http.Client{Transport: errTransport{boom}}}
		if _, err := s.getReadme(ctx, "o/r"); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want wrapped boom", err)
		}
	})
	t.Run("getReadme body read error", func(t *testing.T) {
		s := &Store{BaseURL: "http://gh.test", Client: &http.Client{Transport: bodyErrTransport{}}}
		if _, err := s.getReadme(ctx, "o/r"); err == nil || !strings.Contains(err.Error(), "body read failed") {
			t.Fatalf("err = %v, want body read error", err)
		}
	})
}

func TestGetReadmeStatuses(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		status   int
		header   http.Header
		wantSkip bool
		wantMsg  string
	}{
		{name: "server error", status: http.StatusInternalServerError, wantMsg: "github readme returned status 500"},
		{name: "not found skips", status: http.StatusNotFound, wantSkip: true},
		{name: "rate limited skips", status: http.StatusTooManyRequests, wantSkip: true},
		{name: "forbidden with retry-after skips", status: http.StatusForbidden, header: http.Header{"Retry-After": {"30"}}, wantSkip: true},
		{name: "forbidden with zero remaining skips", status: http.StatusForbidden, header: http.Header{"X-Ratelimit-Remaining": {"0"}}, wantSkip: true},
		{name: "plain forbidden is an error", status: http.StatusForbidden, wantMsg: "github readme returned status 403"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/o/r/readme" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if got := r.Header.Get("Accept"); got != "application/vnd.github.raw" {
					t.Errorf("Accept = %q, want raw readme media type", got)
				}
				for k, v := range tc.header {
					w.Header()[k] = v
				}
				w.WriteHeader(tc.status)
			}))
			defer github.Close()
			s := &Store{BaseURL: github.URL, Client: github.Client()}
			_, err := s.getReadme(ctx, "o/r")
			switch {
			case tc.wantSkip:
				if !errors.Is(err, errSkip) {
					t.Fatalf("err = %v, want errSkip", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("err = %v, want %q", err, tc.wantMsg)
				}
			}
		})
	}
}

// enrich keeps the landscape metadata when the README is unavailable:
// a skip (404/rate-limit) and a hard GitHub error both leave Readme empty
// and are never surfaced as enrichment failures.
func TestEnrichToleratesReadmeFailures(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("readme %d", status), func(t *testing.T) {
			github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r":
					_, _ = w.Write([]byte(`{"description":"from github","topics":["a"],"language":"Go"}`))
				case "/repos/o/r/readme":
					w.WriteHeader(status)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer github.Close()
			s := &Store{BaseURL: github.URL, Client: github.Client()}
			p := Project{RepoID: "o/r", Description: "from landscape"}
			if err := s.enrich(context.Background(), &p); err != nil {
				t.Fatalf("enrich: %v", err)
			}
			if p.Readme != "" {
				t.Fatalf("Readme = %q, want empty when readme fetch fails", p.Readme)
			}
			if p.Description != "from github" || p.Language != "Go" || len(p.Topics) != 1 {
				t.Fatalf("metadata not applied: %+v", p)
			}
		})
	}
}

func TestEnrichReturnsHardRepoError(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer github.Close()
	s := &Store{BaseURL: github.URL, Client: github.Client()}
	p := Project{RepoID: "o/r", Description: "from landscape"}
	err := s.enrich(context.Background(), &p)
	if err == nil || !strings.Contains(err.Error(), "github returned status 500") {
		t.Fatalf("err = %v, want status 500 error", err)
	}
	if p.Description != "from landscape" {
		t.Fatalf("Description = %q, want landscape value preserved", p.Description)
	}
}

func TestRefreshAsyncLogsRefreshError(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer github.Close()

	s := newRefreshStore(t, github, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	logs := make(chan string, 4)
	var once sync.Once
	s.Logf = func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		if strings.HasPrefix(msg, "catalog refresh:") {
			once.Do(func() { logs <- msg })
		}
	}

	s.RefreshAsync()
	select {
	case msg := <-logs:
		if !strings.Contains(msg, "landscape returned status 503") {
			t.Fatalf("log = %q, want landscape status error", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshAsync never logged the refresh failure")
	}

	deadline := time.After(5 * time.Second)
	for {
		s.mu.RLock()
		active, n := s.active, len(s.projects)
		s.mu.RUnlock()
		if !active {
			if n != 0 {
				t.Fatalf("projects = %d, want catalog untouched after failed refresh", n)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("active flag never cleared after failed refresh")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := os.Stat(s.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache file stat err = %v, want not-exist after failed refresh", err)
	}
}

func TestBM25TopKGuards(t *testing.T) {
	projects := []Project{{RepoID: "a/b", Name: "alpha", Description: "service mesh"}}
	b := NewBM25(projects)
	if got := b.TopK("", 3); len(got) != 0 {
		t.Fatalf("TopK(empty query) = %v, want empty", got)
	}
	if got := b.TopK("!!! ---", 3); len(got) != 0 {
		t.Fatalf("TopK(no tokens) = %v, want empty", got)
	}
	if got := b.TopK("mesh", 0); len(got) != 0 {
		t.Fatalf("TopK(k=0) = %v, want empty", got)
	}
	if got := NewBM25(nil).TopK("mesh", 3); len(got) != 0 {
		t.Fatalf("TopK(no projects) = %v, want empty", got)
	}
	if got := b.TopK("mesh", 3); len(got) != 1 || got[0].Project.RepoID != "a/b" {
		t.Fatalf("TopK(mesh) = %v, want the single matching project", got)
	}
}
