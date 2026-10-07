package settle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/store"
)

func TestIssueNumber(t *testing.T) {
	if n, err := IssueNumber("https://github.com/org/repo/issues/42"); err != nil || n != 42 {
		t.Fatalf("got %d, %v; want 42", n, err)
	}
	for _, bad := range []string{"://nope", "https://github.com/org/repo", "https://github.com/org/repo/issues/x", "https://github.com/org/repo/issues/0"} {
		if _, err := IssueNumber(bad); err == nil {
			t.Errorf("IssueNumber(%q) should fail", bad)
		}
	}
}

func TestGithubLogin(t *testing.T) {
	cases := []struct {
		in, login string
		ok        bool
	}{
		{"octocat", "octocat", true},
		{"github:octocat", "octocat", true},
		{"github:", "", false},
		{"gitlab:123", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		login, ok := githubLogin(tc.in)
		if login != tc.login || ok != tc.ok {
			t.Errorf("githubLogin(%q) = %q,%v; want %q,%v", tc.in, login, ok, tc.login, tc.ok)
		}
	}
}

func TestVerifyFiledIssue(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	idea := &store.Idea{Author: "octocat", CreatedAt: created}
	ok := &FiledIssue{Author: "OctoCat", CreatedAt: created.Add(time.Minute)}
	if err := VerifyFiledIssue(ok, idea); err != nil {
		t.Fatalf("matching issue: %v", err)
	}
	if err := VerifyFiledIssue(nil, idea); err == nil {
		t.Error("nil issue must fail")
	}
	if err := VerifyFiledIssue(&FiledIssue{Author: "octocat", CreatedAt: created.Add(time.Minute), PullRequest: true}, idea); err == nil || !strings.Contains(err.Error(), "pull request") {
		t.Errorf("pull request: %v", err)
	}
	if err := VerifyFiledIssue(&FiledIssue{Author: "octocat", CreatedAt: created.Add(-time.Minute)}, idea); err == nil || !strings.Contains(err.Error(), "predates") {
		t.Errorf("older issue: %v", err)
	}
	if err := VerifyFiledIssue(&FiledIssue{Author: "someone", CreatedAt: created.Add(time.Minute)}, idea); err == nil || !strings.Contains(err.Error(), "@octocat") {
		t.Errorf("other author: %v", err)
	}
	// Legacy records without CreatedAt skip the age check.
	if err := VerifyFiledIssue(&FiledIssue{Author: "octocat", CreatedAt: created.Add(-time.Hour)}, &store.Idea{Author: "octocat"}); err != nil {
		t.Errorf("zero CreatedAt: %v", err)
	}
	// Non-GitHub identities have no login to compare, so the issue body
	// must carry this idea's marker — any other issue on the repo is
	// rejected, including one carrying a different idea's marker.
	other := &store.Idea{ID: "idea123abc", Author: "gitlab:9", CreatedAt: created}
	if err := VerifyFiledIssue(&FiledIssue{Author: "anyone", CreatedAt: created.Add(time.Minute), Body: "Some text\n\n---\n" + Footer + "\n" + IdeaMarker("idea123abc")}, other); err != nil {
		t.Errorf("non-github author with marker: %v", err)
	}
	if err := VerifyFiledIssue(&FiledIssue{Author: "anyone", CreatedAt: created.Add(time.Minute)}, other); err == nil || !strings.Contains(err.Error(), "marker") {
		t.Errorf("non-github author without marker: %v", err)
	}
	if err := VerifyFiledIssue(&FiledIssue{Author: "anyone", CreatedAt: created.Add(time.Minute), Body: IdeaMarker("zzzzzzzzzz")}, other); err == nil || !strings.Contains(err.Error(), "marker") {
		t.Errorf("non-github author with another idea's marker: %v", err)
	}
	// A GitHub ideator is verified by login; the marker is not required.
	if err := VerifyFiledIssue(&FiledIssue{Author: "octocat", CreatedAt: created.Add(time.Minute), Body: ""}, &store.Idea{ID: "idea123abc", Author: "octocat", CreatedAt: created}); err != nil {
		t.Errorf("github author without marker: %v", err)
	}
}

func TestIdeaMarker(t *testing.T) {
	m := IdeaMarker("abc123")
	if m != "<!-- dibs-idea: abc123 -->" {
		t.Fatalf("marker = %q", m)
	}
	if !HasIdeaMarker("x\n"+m+"\n", "abc123") {
		t.Error("HasIdeaMarker must find the marker anywhere in the body")
	}
	if HasIdeaMarker(m, "abc12") || HasIdeaMarker(m, "") || HasIdeaMarker("", "abc123") {
		t.Error("HasIdeaMarker must not match a prefix, an empty id, or an empty body")
	}
}

func TestLaunchBodyForMarker(t *testing.T) {
	got := LaunchBodyFor("Idea text", true, "abc123")
	if !strings.HasSuffix(got, "---\n"+Footer+"\n"+IdeaMarker("abc123")) {
		t.Fatalf("body must end with footer then marker:\n%s", got)
	}
	if LaunchBodyFor(got, true, "abc123") != got {
		t.Error("LaunchBodyFor must be idempotent when a marker already trails the footer")
	}
	if strings.Count(LaunchBodyFor(LaunchBody("x", false), false, "abc123"), ExternalFooter) != 1 {
		t.Error("an existing footer without marker must not be duplicated")
	}
	if strings.Contains(LaunchBodyFor("x", true, ""), markerPrefix) {
		t.Error("empty ideaID must not emit a marker")
	}
	if stripTrailingMarker("plain -->") != "plain -->" {
		t.Error("a trailing --> without a marker line must be left alone")
	}
}

func TestNewIssueURLForPreservesMarkerWhenTruncated(t *testing.T) {
	huge := LaunchBodyFor(strings.Repeat("word ", 4000), false, "abc123")
	got, truncated := NewIssueURLFor("org/repo", "Big", huge, false, "abc123")
	if !truncated {
		t.Fatal("expected truncation")
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	body := u.Query().Get("body")
	if !strings.HasSuffix(body, ExternalFooter+"\n"+IdeaMarker("abc123")) {
		t.Fatalf("truncated body must keep footer and marker:\n%.120s...%s", body, body[len(body)-80:])
	}
}

func TestHTTPClientGetIssue(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		switch r.URL.Path {
		case "/repos/org/repo/issues/1":
			_, _ = w.Write([]byte(`{"user":{"login":"octocat"},"created_at":"2026-09-01T12:00:00Z","body":"hello <!-- dibs-idea: abc -->"}`))
		case "/repos/org/repo/issues/2":
			_, _ = w.Write([]byte(`{"user":{"login":"octocat"},"created_at":"2026-09-01T12:00:00Z","pull_request":{"url":"x"}}`))
		case "/repos/org/repo/issues/3":
			w.WriteHeader(http.StatusGone)
		case "/repos/org/repo/issues/4":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	c := &HTTPClient{BaseURL: srv.URL}
	is, err := c.GetIssue(ctx, "org/repo", 1)
	if err != nil || is.Author != "octocat" || is.PullRequest || !is.CreatedAt.Equal(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)) || !HasIdeaMarker(is.Body, "abc") {
		t.Fatalf("issue 1: %+v, %v", is, err)
	}
	if gotAuth != "" || gotPath != "/repos/org/repo/issues/1" {
		t.Fatalf("anonymous lookup sent auth=%q path=%q", gotAuth, gotPath)
	}
	if is, err := c.GetIssue(ctx, "org/repo", 2); err != nil || !is.PullRequest {
		t.Fatalf("issue 2 should be a pull request: %+v, %v", is, err)
	}
	for _, n := range []int{3, 99} {
		if _, err := c.GetIssue(ctx, "org/repo", n); !errors.Is(err, ErrIssueNotFound) {
			t.Errorf("issue %d: err=%v, want ErrIssueNotFound", n, err)
		}
	}
	if _, err := c.GetIssue(ctx, "org/repo", 4); err == nil || errors.Is(err, ErrIssueNotFound) || !strings.Contains(err.Error(), "status 403") {
		t.Errorf("issue 4: err=%v, want status error", err)
	}

	withToken := &HTTPClient{BaseURL: srv.URL, Token: "tok"}
	if _, err := withToken.GetIssue(ctx, "org/repo", 1); err != nil {
		t.Fatalf("with token: %v", err)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("token lookup sent auth=%q", gotAuth)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if _, err := (&HTTPClient{BaseURL: closed.URL}).GetIssue(ctx, "org/repo", 1); err == nil || errors.Is(err, ErrIssueNotFound) {
		t.Errorf("unreachable: err=%v, want transport error", err)
	}
}

func TestFakeIssueLookup(t *testing.T) {
	f := &FakeIssueLookup{Issues: map[string]FiledIssue{"org/repo#1": {Author: "octocat"}}}
	is, err := f.GetIssue(context.Background(), "org/repo", 1)
	if err != nil || is.Author != "octocat" {
		t.Fatalf("known issue: %+v, %v", is, err)
	}
	is.Author = "mutated"
	if again, _ := f.GetIssue(context.Background(), "org/repo", 1); again.Author != "octocat" {
		t.Fatal("GetIssue must return a copy")
	}
	if _, err := f.GetIssue(context.Background(), "org/repo", 2); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("unknown issue: %v", err)
	}
	boom := errors.New("boom")
	if _, err := (&FakeIssueLookup{Err: boom}).GetIssue(context.Background(), "org/repo", 1); !errors.Is(err, boom) {
		t.Fatalf("Err: %v", err)
	}
}
