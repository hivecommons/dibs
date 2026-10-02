package settle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHTTPClientDoErrors pins every failure arm of the legacy GitHub
// transport: an unmarshalable payload, an unbuildable request, an
// unreachable host, a truncated body, and a 2xx response that is not JSON.
// Each error is wrapped with a "settle:" prefix so callers can tell transport
// faults from GitHub's own status-code rejections.
func TestHTTPClientDoErrors(t *testing.T) {
	t.Run("marshal", func(t *testing.T) {
		c := &HTTPClient{Token: "tok", BaseURL: "http://127.0.0.1:0"}
		status, err := c.do(context.Background(), http.MethodPost, "/x", make(chan int), nil)
		if status != 0 || err == nil || !strings.Contains(err.Error(), "settle: marshaling") {
			t.Fatalf("status=%d err=%v, want marshaling error", status, err)
		}
	})
	t.Run("build request", func(t *testing.T) {
		c := &HTTPClient{Token: "tok", BaseURL: "http://127.0.0.1:0"}
		status, err := c.do(context.Background(), "BAD METHOD", "/x", nil, nil)
		if status != 0 || err == nil || !strings.Contains(err.Error(), "settle: building request") {
			t.Fatalf("status=%d err=%v, want building request error", status, err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close() // nothing listens here any more
		c := &HTTPClient{Token: "tok", BaseURL: srv.URL}
		status, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil)
		if status != 0 || err == nil || !strings.Contains(err.Error(), "settle: github unreachable") {
			t.Fatalf("status=%d err=%v, want unreachable error", status, err)
		}
	})
	t.Run("truncated body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Promise more bytes than we send: the client's ReadAll sees an
			// unexpected EOF once the server closes the connection.
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("{"))
		}))
		defer srv.Close()
		c := &HTTPClient{Token: "tok", BaseURL: srv.URL}
		status, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil)
		if status != http.StatusCreated || err == nil || !strings.Contains(err.Error(), "settle: reading github response") {
			t.Fatalf("status=%d err=%v, want reading response error", status, err)
		}
	})
	t.Run("non-json 2xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("<html>not json</html>"))
		}))
		defer srv.Close()
		c := &HTTPClient{Token: "tok", BaseURL: srv.URL}
		var out struct{}
		status, err := c.do(context.Background(), http.MethodGet, "/x", nil, &out)
		if status != http.StatusCreated || err == nil || !strings.Contains(err.Error(), "settle: decoding github response") {
			t.Fatalf("status=%d err=%v, want decoding error", status, err)
		}
	})
	t.Run("non-2xx body is not decoded", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<html>forbidden</html>"))
		}))
		defer srv.Close()
		c := &HTTPClient{Token: "tok", BaseURL: srv.URL}
		var out struct{}
		status, err := c.do(context.Background(), http.MethodGet, "/x", nil, &out)
		if status != http.StatusForbidden || err != nil {
			t.Fatalf("status=%d err=%v, want 403 and no decode attempt", status, err)
		}
	})
	t.Run("default base url and client", func(t *testing.T) {
		// Empty BaseURL falls back to api.github.com; the request must never
		// reach it because the context is already cancelled.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := &HTTPClient{Token: "tok"}
		_, err := c.do(ctx, http.MethodGet, "/x", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "settle: github unreachable") {
			t.Fatalf("err=%v, want unreachable error from cancelled context", err)
		}
	})
}

// TestEnsureLabelStatuses: only 201 and 422 (already exists) are success;
// transport errors propagate unchanged and other statuses become errors
// naming the repo.
func TestEnsureLabelStatuses(t *testing.T) {
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/labels" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := &HTTPClient{Token: "tok", BaseURL: srv.URL}

	for _, ok := range []int{http.StatusCreated, http.StatusUnprocessableEntity} {
		status = ok
		if err := c.EnsureLabel(context.Background(), "org/repo", Label, LabelColor, LabelDescription); err != nil {
			t.Fatalf("status %d: %v, want success", ok, err)
		}
	}
	status = http.StatusNotFound
	err := c.EnsureLabel(context.Background(), "org/repo", Label, LabelColor, LabelDescription)
	if err == nil || !strings.Contains(err.Error(), "creating label on org/repo: status 404") {
		t.Fatalf("status 404: %v, want labelled status error", err)
	}

	srv.Close()
	err = c.EnsureLabel(context.Background(), "org/repo", Label, LabelColor, LabelDescription)
	if err == nil || !strings.Contains(err.Error(), "settle: github unreachable") {
		t.Fatalf("closed server: %v, want unreachable error", err)
	}
}

// TestCreateIssueStatuses: a non-201 response is an error carrying the repo
// and status; transport errors propagate; a 201 with no html_url yields "".
func TestCreateIssueStatuses(t *testing.T) {
	var status int
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/issues" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type %q", ct)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &HTTPClient{Token: "tok", BaseURL: srv.URL}

	status, body = http.StatusForbidden, `{"message":"forbidden"}`
	url, err := c.CreateIssue(context.Background(), "org/repo", "t", "b", []string{Label})
	if url != "" || err == nil || !strings.Contains(err.Error(), "creating issue on org/repo: status 403") {
		t.Fatalf("403: url=%q err=%v", url, err)
	}

	status, body = http.StatusCreated, `{}`
	url, err = c.CreateIssue(context.Background(), "org/repo", "t", "b", []string{Label})
	if err != nil || url != "" {
		t.Fatalf("201 without html_url: url=%q err=%v, want empty url and nil error", url, err)
	}

	status, body = http.StatusCreated, `not json`
	url, err = c.CreateIssue(context.Background(), "org/repo", "t", "b", []string{Label})
	if url != "" || err == nil || !strings.Contains(err.Error(), "settle: decoding github response") {
		t.Fatalf("201 non-json: url=%q err=%v", url, err)
	}

	srv.Close()
	url, err = c.CreateIssue(context.Background(), "org/repo", "t", "b", []string{Label})
	if url != "" || err == nil || !strings.Contains(err.Error(), "settle: github unreachable") {
		t.Fatalf("closed server: url=%q err=%v", url, err)
	}
}

// TestFakeFailErr: when FailErr is set both Fake methods return it verbatim
// and record nothing, and Settle surfaces it before any issue is created.
func TestFakeFailErr(t *testing.T) {
	boom := errors.New("boom")
	fake := &Fake{FailErr: boom}

	if err := fake.EnsureLabel(context.Background(), "org/repo", Label, LabelColor, LabelDescription); !errors.Is(err, boom) {
		t.Fatalf("EnsureLabel err=%v, want boom", err)
	}
	url, err := fake.CreateIssue(context.Background(), "org/repo", "t", "b", []string{Label})
	if url != "" || !errors.Is(err, boom) {
		t.Fatalf("CreateIssue url=%q err=%v, want empty url and boom", url, err)
	}
	if len(fake.Labels) != 0 || len(fake.Issues) != 0 {
		t.Fatalf("failing fake recorded state: labels=%v issues=%v", fake.Labels, fake.Issues)
	}

	s := &Settler{GitHub: fake}
	if url, err := s.Settle(context.Background(), testIdea(), "org/repo"); url != "" || !errors.Is(err, boom) {
		t.Fatalf("Settle url=%q err=%v, want boom from EnsureLabel", url, err)
	}
}

// TestIssueBodyFallsBackToHandle: with no display name the attribution line
// repeats the handle instead of printing an empty parenthetical, and the TLDR
// block is omitted when the idea has none.
func TestIssueBodyFallsBackToHandle(t *testing.T) {
	idea := testIdea()
	idea.AuthorDisplay = ""
	idea.TLDR = ""
	body := IssueBody(idea)
	if !strings.Contains(body, "**Idea by @josh** (josh)") {
		t.Fatalf("attribution did not fall back to handle:\n%s", body)
	}
	if strings.Contains(body, "**TLDR:**") {
		t.Fatalf("TLDR block rendered for an idea without one:\n%s", body)
	}
}
