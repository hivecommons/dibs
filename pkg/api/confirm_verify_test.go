package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

// confirmFixture returns an API whose confirm-issue path verifies against
// lookup, plus an ACCEPTED idea by author targeting kubestellar/dibs.
func confirmFixture(t *testing.T, author string, lookup settle.IssueLookup) (*API, *http.ServeMux, *store.Idea) {
	t.Helper()
	a, mux := newAPIFixture(t)
	a.Issues = lookup
	idea := mustCreate(t, a, author, "Verified settlement", store.VisibilityPublic, store.StatusAccepted)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	return a, mux, idea
}

func confirm(t *testing.T, mux *http.ServeMux, user, ideaID, issueURL string) (int, string) {
	t.Helper()
	rec := do(t, mux, ident(user), "POST", "/api/ideas/"+ideaID+"/confirm-issue", `{"issueURL":"`+issueURL+`"}`)
	return rec.Code, rec.Body.String()
}

// TestConfirmIssueVerifiesAgainstGitHub pins the verification gate: a
// shape-valid URL settles only when GitHub says the issue exists, is an
// issue, is at least as new as the idea, and was opened by the ideator.
func TestConfirmIssueVerifiesAgainstGitHub(t *testing.T) {
	later := time.Now().UTC().Add(time.Hour)
	earlier := time.Now().UTC().Add(-24 * time.Hour)
	lookup := &settle.FakeIssueLookup{Issues: map[string]settle.FiledIssue{
		"kubestellar/dibs#1": {Author: "Bob", CreatedAt: later},
		"kubestellar/dibs#2": {Author: "mallory", CreatedAt: later},
		"kubestellar/dibs#3": {Author: "bob", CreatedAt: earlier},
		"kubestellar/dibs#4": {Author: "bob", CreatedAt: later, PullRequest: true},
	}}
	a, mux, idea := confirmFixture(t, "bob", lookup)

	cases := []struct {
		name, url, want string
	}{
		{"someone else's issue", "https://github.com/kubestellar/dibs/issues/2", "not opened by @bob"},
		{"issue older than the idea", "https://github.com/kubestellar/dibs/issues/3", "predates"},
		{"pull request", "https://github.com/kubestellar/dibs/issues/4", "pull request"},
		{"missing issue", "https://github.com/kubestellar/dibs/issues/99", "does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := confirm(t, mux, "bob", idea.ID, tc.url)
			if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Fatalf("status=%d body=%s, want 400 containing %q", code, body, tc.want)
			}
			got, err := a.Store.Get(idea.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != store.StatusAccepted || got.IssueURL != "" {
				t.Fatalf("rejected confirm must not settle: status=%s issueURL=%q", got.Status, got.IssueURL)
			}
		})
	}

	// The ideator's own issue (login compared case-insensitively) settles.
	code, body := confirm(t, mux, "bob", idea.ID, "https://github.com/kubestellar/dibs/issues/1")
	if code != http.StatusOK {
		t.Fatalf("own issue: status=%d body=%s", code, body)
	}
	got, err := a.Store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusSettled || got.IssueURL != "https://github.com/kubestellar/dibs/issues/1" {
		t.Fatalf("own issue must settle: status=%s issueURL=%q", got.Status, got.IssueURL)
	}
}

// TestConfirmIssueGitHubOutageIs502 covers a lookup failure that is neither
// "not found" nor a verification verdict: the idea stays accepted and the
// caller is told to retry rather than being handed a 400 they cannot fix.
func TestConfirmIssueGitHubOutageIs502(t *testing.T) {
	lookup := &settle.FakeIssueLookup{Err: errors.New("settle: github unreachable")}
	a, mux, idea := confirmFixture(t, "bob", lookup)
	code, body := confirm(t, mux, "bob", idea.ID, "https://github.com/kubestellar/dibs/issues/1")
	if code != http.StatusBadGateway || !strings.Contains(body, "could not verify") {
		t.Fatalf("status=%d body=%s, want 502", code, body)
	}
	if strings.Contains(body, "unreachable") {
		t.Fatalf("transport detail must not reach the client: %s", body)
	}
	got, err := a.Store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusAccepted {
		t.Fatalf("outage must not settle: status=%s", got.Status)
	}
}

// TestConfirmIssueNonGitHubIdentityRequiresMarker: an ideator who signed
// in through another provider has no GitHub login to compare, so the issue
// body must carry the idea marker the launch footer embeds; any other
// issue on the repo is rejected and the idea stays unsettled.
func TestConfirmIssueNonGitHubIdentityRequiresMarker(t *testing.T) {
	lookup := &settle.FakeIssueLookup{Issues: map[string]settle.FiledIssue{
		"kubestellar/dibs#7": {Author: "whoever", CreatedAt: time.Now().UTC().Add(time.Hour)},
	}}
	a, mux, idea := confirmFixture(t, "gitlab:12345", lookup)
	code, body := confirm(t, mux, "gitlab:12345", idea.ID, "https://github.com/kubestellar/dibs/issues/7")
	if code != http.StatusBadRequest || !strings.Contains(body, "marker") {
		t.Fatalf("status=%d body=%s, want 400 naming the marker", code, body)
	}
	if got, err := a.Store.Get(idea.ID); err != nil || got.Status != store.StatusAccepted {
		t.Fatalf("unmarked issue must not settle: status=%v err=%v", got.Status, err)
	}

	lookup.Issues["kubestellar/dibs#8"] = settle.FiledIssue{
		Author:    "whoever",
		CreatedAt: time.Now().UTC().Add(time.Hour),
		Body:      settle.LaunchBodyFor("filed from the launch step", true, idea.ID),
	}
	code, body = confirm(t, mux, "gitlab:12345", idea.ID, "https://github.com/kubestellar/dibs/issues/8")
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", code, body)
	}
}
