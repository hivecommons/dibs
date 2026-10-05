package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// errBody is a request body whose Read always fails, driving decodeInput's
// "reading request body" arm (the one branch strings.Reader can never hit).
type errBody struct{}

var _ io.ReadCloser = errBody{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("read: connection reset") }
func (errBody) Close() error             { return nil }

func TestDecodeInputBodyReadErrorIs400(t *testing.T) {
	_, mux := newAPIFixture(t)
	req := httptest.NewRequest("PUT", "/api/repos/kubestellar/dibs", errBody{})
	req = req.WithContext(auth.WithIdentity(req.Context(), ident("alice")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reading request body") {
		t.Fatalf("unreadable body: status=%d body=%s, want 400 reading request body", rec.Code, rec.Body.String())
	}
}

// TestUpdateRepoRejectsBadInput pins the two handleUpdateRepo arms that are
// neither not-found nor forbidden: a malformed body is a 400 from
// decodeInput, and a registry validation failure (too many topics, blank
// topic, oversized appetite) is a 400 carrying the registry's message.
func TestUpdateRepoRejectsBadInput(t *testing.T) {
	a, mux := newAPIFixture(t)

	rec := do(t, mux, ident("alice"), "PUT", "/api/repos/kubestellar/dibs", `{"appetite":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}

	tooMany := make([]string, registry.MaxTopics+1)
	for i := range tooMany {
		tooMany[i] = "t"
	}
	cases := []struct{ name, body, want string }{
		{"too many topics", `{"topics":["` + strings.Join(tooMany, `","`) + `"]}`, "more than"},
		{"blank topic", `{"topics":["ok","  "]}`, "cannot be blank"},
		{"long topic", `{"topics":["` + strings.Repeat("x", registry.MaxTopicLen+1) + `"]}`, "topic exceeds"},
		{"long appetite", `{"appetite":"` + strings.Repeat("a", registry.MaxAppetiteLen+1) + `"}`, "appetite exceeds"},
	}
	for _, tc := range cases {
		rec := do(t, mux, ident("alice"), "PUT", "/api/repos/kubestellar/dibs", tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s: status=%d body=%s, want 400 containing %q", tc.name, rec.Code, rec.Body.String(), tc.want)
		}
	}

	// None of the rejected updates may have touched the profile.
	rp, err := a.Registry.Get("kubestellar/dibs")
	if err != nil {
		t.Fatalf("Registry.Get: %v", err)
	}
	if len(rp.Topics) > registry.MaxTopics || len(rp.Appetite) > registry.MaxAppetiteLen {
		t.Fatalf("rejected update leaked into profile: %+v", rp)
	}
}

// TestHandleRepoQRRegistryFailure pins that a registry failure other than
// not-found on the public QR route is a generic 500 and never leaks the
// underlying error text into the (image) response.
func TestHandleRepoQRRegistryFailure(t *testing.T) {
	a, _ := newAPIFixture(t)
	a.Registry = &faultRegistry{RepoRegistry: a.Registry, getErr: errBoom}
	rec := httptest.NewRecorder()
	a.HandleRepoQR(rec, repoPathReq("/api/repos/kubestellar/dibs/qr.png", "kubestellar", "dibs"))
	want500(t, rec, "qr registry failure")
	if strings.Contains(rec.Body.String(), errBoom.Error()) {
		t.Fatalf("leaked internal error text: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "image/") {
		t.Fatalf("error response advertised Content-Type %q", ct)
	}
}

// TestLaunchStoreTransitionFailure covers the only store write in
// handleLaunch: the accepted → issue_launched transition. When it fails
// the handler must return 500 and the idea must still be accepted (the
// issue URL is computed client-side, so nothing else has happened yet).
func TestLaunchStoreTransitionFailure(t *testing.T) {
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	idea := mustCreate(t, a, "bob", "Launch me", store.VisibilityPublic, store.StatusAccepted)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	withFaultStore(a, "Transition")

	rec := do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/launch", `{}`)
	want500(t, rec, "launch transition")
	if strings.Contains(rec.Body.String(), errBoom.Error()) {
		t.Fatalf("leaked internal error text: %s", rec.Body.String())
	}
	stored, err := a.Store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != store.StatusAccepted {
		t.Fatalf("status after failed launch = %q, want accepted", stored.Status)
	}
}

// TestConfirmIssueStoreFailure covers the settle Mutate in
// handleConfirmIssue failing after every input gate has passed: 500, and
// the idea keeps its pre-confirm status with no issue URL recorded.
func TestConfirmIssueStoreFailure(t *testing.T) {
	now := time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	idea := mustCreate(t, a, "bob", "Confirm me", store.VisibilityPublic, store.StatusIssueLaunched)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	withFaultStore(a, "Mutate")

	rec := do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/confirm-issue",
		`{"issueURL":"https://github.com/kubestellar/dibs/issues/7"}`)
	want500(t, rec, "confirm-issue settle")
	stored, err := a.Store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != store.StatusIssueLaunched || stored.IssueURL != "" {
		t.Fatalf("idea after failed confirm = status %q issueURL %q, want issue_launched and empty", stored.Status, stored.IssueURL)
	}
}
