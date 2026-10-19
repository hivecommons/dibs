package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

// The decide and settle handlers read the idea, check it, and then re-check
// inside Store.Mutate because another request can commit between the two.
// These tests reproduce that window with a store wrapper that commits a
// competing write right before delegating the Nth Mutate call, and pin
// that the second check turns the stale request into a 400 with nothing
// persisted — Mutate drops the write when fn returns an error.

// mutateInterposer runs before() once, right before the onCall-th Mutate
// (1-based) reaches the underlying store.
type mutateInterposer struct {
	IdeaStore
	onCall int
	calls  int
	before func()
}

func (s *mutateInterposer) Mutate(id string, touch bool, fn func(*store.Idea) error) (*store.Idea, error) {
	s.calls++
	if s.calls == s.onCall && s.before != nil {
		s.before()
	}
	return s.IdeaStore.Mutate(id, touch, fn)
}

// seedPendingOffer puts a pending offer for repoID on an offered idea.
func seedPendingOffer(t *testing.T, st IdeaStore, id, repoID string, now time.Time) {
	t.Helper()
	if _, err := st.Mutate(id, false, func(i *store.Idea) error {
		i.Offers = []store.Offer{{RepoID: repoID, Status: store.OfferPending, CreatedAt: now}}
		return nil
	}); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
}

// TestDeclineLosesRaceToAccept: alice's decline loads a pending offer, but
// her own concurrent accept commits first. The decline must fail closed
// instead of flipping an accepted idea to declined or notifying bob.
func TestDeclineLosesRaceToAccept(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = ns
	idea := mustCreate(t, a, "bob", "Raced decline", store.VisibilityPublic, store.StatusOffered)
	seedPendingOffer(t, a.Store, idea.ID, "kubestellar/dibs", now)

	real := a.Store
	a.Store = &mutateInterposer{IdeaStore: real, onCall: 1, before: func() {
		if _, err := real.Mutate(idea.ID, true, func(i *store.Idea) error {
			o := i.OfferTo("kubestellar/dibs")
			o.Status = store.OfferAccepted
			o.DecidedAt = &now
			i.TargetRepo = "kubestellar/dibs"
			return i.TransitionTo(store.StatusAccepted, "accept")
		}); err != nil {
			t.Fatalf("competing accept: %v", err)
		}
	}}

	rec := do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide", `{"ideaID":"`+idea.ID+`","decision":"decline"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no pending offer") {
		t.Fatalf("raced decline: status=%d body=%s, want 400 naming the missing offer", rec.Code, rec.Body.String())
	}
	got, err := real.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusAccepted || got.OfferTo("kubestellar/dibs").Status != store.OfferAccepted {
		t.Fatalf("raced decline overwrote the accept: status=%s offer=%+v", got.Status, got.OfferTo("kubestellar/dibs"))
	}
	if n := ns.ListByUser("bob", false); len(n) != 0 {
		t.Fatalf("raced decline must not notify the ideator, got %+v", n)
	}
}

// TestAcceptLosesRaceToDecline: the mirror image — alice's accept passes
// the pending-offer pre-check, then the offer is declined before the
// write. The idea must stay offered/declined, not become accepted.
func TestAcceptLosesRaceToDecline(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 5, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = ns
	idea := mustCreate(t, a, "bob", "Raced accept", store.VisibilityPublic, store.StatusOffered)
	seedPendingOffer(t, a.Store, idea.ID, "kubestellar/dibs", now)

	real := a.Store
	a.Store = &mutateInterposer{IdeaStore: real, onCall: 1, before: func() {
		if _, err := real.Mutate(idea.ID, true, func(i *store.Idea) error {
			o := i.OfferTo("kubestellar/dibs")
			o.Status = store.OfferDeclined
			o.DecidedAt = &now
			i.TryTransition(store.StatusDeclined)
			return nil
		}); err != nil {
			t.Fatalf("competing decline: %v", err)
		}
	}}

	rec := do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide", `{"ideaID":"`+idea.ID+`","decision":"accept"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no pending offer") {
		t.Fatalf("raced accept: status=%d body=%s, want 400 naming the missing offer", rec.Code, rec.Body.String())
	}
	got, err := real.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusDeclined || got.TargetRepo != "" {
		t.Fatalf("raced accept took effect: status=%s target=%q", got.Status, got.TargetRepo)
	}
	if n := ns.ListByUser("bob", false); len(n) != 0 {
		t.Fatalf("raced accept must not notify the ideator, got %+v", n)
	}
}

// TestLegacySettleRefusesIllegalTransition: in token mode the accept
// commits (Mutate #1), the GitHub issue is opened, and the settle write
// (Mutate #2) re-runs the state machine. If the idea is no longer in a
// state that may settle, the handler reports the rejected transition and
// does not stamp IssueURL/SettledAt — rather than trusting the status it
// read before calling GitHub.
func TestLegacySettleRefusesIllegalTransition(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 10, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = ns
	fake := &settle.Fake{}
	a.Settler = &settle.Settler{GitHub: fake}
	idea := mustCreate(t, a, "bob", "Legacy raced", store.VisibilityPublic, store.StatusOffered)
	seedPendingOffer(t, a.Store, idea.ID, "kubestellar/dibs", now)

	real := a.Store
	a.Store = &mutateInterposer{IdeaStore: real, onCall: 2, before: func() {
		// Direct store write: no API path leaves accepted for a state that
		// cannot settle, so this stands in for an out-of-band repair.
		if _, err := real.Mutate(idea.ID, true, func(i *store.Idea) error {
			i.Status = store.StatusDraft
			return nil
		}); err != nil {
			t.Fatalf("competing reset: %v", err)
		}
	}}

	rec := do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide", `{"ideaID":"`+idea.ID+`","decision":"accept"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cannot settle an idea in status draft") {
		t.Fatalf("legacy settle after reset: status=%d body=%s, want 400 naming the transition", rec.Code, rec.Body.String())
	}
	if len(fake.Issues) != 1 {
		t.Fatalf("GitHub issue calls = %d, want exactly 1 before the settle write", len(fake.Issues))
	}
	got, err := real.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusDraft || got.IssueURL != "" || !got.SettledAt.IsZero() {
		t.Fatalf("settle write landed despite the rejected transition: %+v", got)
	}
	for _, n := range ns.ListByUser("bob", false) {
		if n.Kind == notify.KindIssue {
			t.Fatalf("issue notification sent for an unsettled idea: %+v", n)
		}
	}
}

// TestConfirmIssueRefusesIllegalTransition: the ideator's confirm passes
// the accepted/issue_launched pre-check, then the settle write finds the
// idea in a state the machine will not settle from. The 400 must name the
// transition and the idea must keep no IssueURL.
func TestConfirmIssueRefusesIllegalTransition(t *testing.T) {
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = ns
	idea := mustCreate(t, a, "bob", "Confirm raced", store.VisibilityPublic, store.StatusAccepted)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	real := a.Store
	a.Store = &mutateInterposer{IdeaStore: real, onCall: 1, before: func() {
		if _, err := real.Mutate(idea.ID, true, func(i *store.Idea) error {
			i.Status = store.StatusDeclined
			return nil
		}); err != nil {
			t.Fatalf("competing reset: %v", err)
		}
	}}

	rec := do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/confirm-issue", `{"issueURL":"https://github.com/kubestellar/dibs/issues/12"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cannot settle an idea in status declined") {
		t.Fatalf("confirm after reset: status=%d body=%s, want 400 naming the transition", rec.Code, rec.Body.String())
	}
	got, err := real.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusDeclined || got.IssueURL != "" || !got.SettledAt.IsZero() {
		t.Fatalf("settle write landed despite the rejected transition: %+v", got)
	}
	if n := ns.ListByUser("alice", false); len(n) != 0 {
		t.Fatalf("repo owner notified of an unsettled idea: %+v", n)
	}
}
