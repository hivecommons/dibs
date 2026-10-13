// Regression pins for case-insensitive repo-owner comparison (#369). The
// registry's Owner comes from the hub and the caller's Username from the
// auth provider; the two may differ only in case, and three sites in this
// package compare them. offeredToCallerRepo is pinned in
// offers_edge_test.go; these cover the other two.
package api

import (
	"net/http"
	"testing"

	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/store"
)

// TestOwnedRepoIsCaseInsensitive: a mixed-case login for the registered
// owner "alice" passes ownedRepo on both routes that use it, while a
// different user is still refused.
func TestOwnedRepoIsCaseInsensitive(t *testing.T) {
	_, mux := newAPIFixture(t)

	rec := do(t, mux, ident("Alice"), "GET", "/api/repos/kubestellar/dibs/feed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("mixed-case owner feed: status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, ident("ALICE"), "POST", "/api/repos/kubestellar/dibs/decide", `{"ideaID":"nope","decision":"accept"}`)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("mixed-case owner decide: got 403, ownership must compare case-insensitively")
	}
	rec = do(t, mux, ident("mallory"), "GET", "/api/repos/kubestellar/dibs/feed", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner feed: status=%d, want 403", rec.Code)
	}
}

// TestConfirmIssueSelfNotifyIsCaseInsensitive: when the ideator and the
// accepting repo's owner are the same person under different casing,
// confirm-issue must still skip the "issue landed" notification — the
// owner should not be told about an issue they filed themselves.
func TestConfirmIssueSelfNotifyIsCaseInsensitive(t *testing.T) {
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = ns

	own := mustCreate(t, a, "Alice", "Self accepted, mixed case", store.VisibilityPublic, store.StatusAccepted)
	if _, err := a.Store.Mutate(own.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	rec := do(t, mux, ident("Alice"), "POST", "/api/ideas/"+own.ID+"/confirm-issue", `{"issueURL":"https://github.com/kubestellar/dibs/issues/9"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("self confirm: status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, user := range []string{"alice", "Alice"} {
		if got := ns.ListByUser(user, true); len(got) != 0 {
			t.Fatalf("owner==author (case-folded) must not self-notify %q, got %+v", user, got)
		}
	}
}
