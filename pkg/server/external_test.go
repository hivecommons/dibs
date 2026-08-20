package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kubestellar/dibs/pkg/settle"
	"github.com/kubestellar/dibs/pkg/store"
)

type launchResp struct {
	URL       string `json:"url"`
	FullBody  string `json:"fullBody"`
	Truncated bool   `json:"truncated"`
}

// TestExternalRepoOfferLaunchSettle: the ideator may target ANY GitHub repo,
// not just registry (hive-managed) ones. External targets have no repo
// owner on our side, so they skip acceptance and go straight to the launch
// flow (offered → issue_launched → settled) — and the launched issue body
// carries the "request a hive" growth CTA instead of the short hive footer.
func TestExternalRepoOfferLaunchSettle(t *testing.T) {
	f := newWave2Server(t, nil)
	idea := f.createIdea(t, "bob-session", "External-bound idea", "kubernetes widget body", "private")
	const ext = "someorg/somerepo" // NOT in the registry

	// A malformed external target is rejected.
	rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": "not a repo!!"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed external repo: want 400, got %d %s", rec.Code, rec.Body.String())
	}

	// Offer to the external repo: offered, target set, offer marked external.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": ext})
	if rec.Code != http.StatusOK {
		t.Fatalf("external offer: %d %s", rec.Code, rec.Body.String())
	}
	got := decode[store.Idea](t, rec)
	if got.Status != store.StatusOffered || got.TargetRepo != ext {
		t.Fatalf("after external offer: status=%q target=%q", got.Status, got.TargetRepo)
	}
	if o := got.OfferTo(ext); o == nil || !o.External || o.Status != store.OfferPending {
		t.Fatalf("external offer record wrong: %+v", got.Offers)
	}

	// Double-offer rejected, like the hive path.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": ext})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("double external offer: want 400, got %d", rec.Code)
	}

	// Privacy invariant: the private, externally-offered idea stays
	// invisible to everyone else — browse and the public board.
	rec = doJSON(t, f.h, "GET", "/api/ideas?scope=public", "alice-session", nil)
	if strings.Contains(rec.Body.String(), "External-bound idea") {
		t.Fatalf("private external-offered idea leaked into browse: %s", rec.Body.String())
	}
	rec = doJSON(t, f.h, "GET", "/api/board", "", nil)
	if strings.Contains(rec.Body.String(), "External-bound idea") {
		t.Fatalf("private external-offered idea leaked onto the board: %s", rec.Body.String())
	}

	// Launch straight from "offered" — no acceptance step exists for
	// external targets.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/launch", "bob-session", map[string]string{})
	if rec.Code != http.StatusOK {
		t.Fatalf("external launch: %d %s", rec.Code, rec.Body.String())
	}
	launch := decode[launchResp](t, rec)
	if !strings.HasPrefix(launch.URL, "https://github.com/"+ext+"/issues/new?") {
		t.Fatalf("external launch url: %q", launch.URL)
	}
	u, err := url.Parse(launch.URL)
	if err != nil {
		t.Fatalf("parsing launch url: %v", err)
	}
	body := u.Query().Get("body")
	// The growth loop: non-hive issues advertise hive.
	if !strings.HasSuffix(launch.FullBody, settle.ExternalFooter) ||
		!strings.Contains(body, "requesting a hive") {
		t.Fatalf("external launch body missing the hive CTA:\n%s", launch.FullBody)
	}
	if strings.HasSuffix(launch.FullBody, settle.Footer) {
		t.Fatalf("external launch body must not use the hive footer:\n%s", launch.FullBody)
	}
	if st, _ := f.store.Get(idea.ID); st.Status != store.StatusIssueLaunched {
		t.Fatalf("after external launch: %+v", st)
	}

	// Confirm the filed issue → settled on the external repo.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/confirm-issue", "bob-session",
		map[string]string{"issueURL": "https://github.com/" + ext + "/issues/7"})
	if rec.Code != http.StatusOK {
		t.Fatalf("external confirm: %d %s", rec.Code, rec.Body.String())
	}
	if st, _ := f.store.Get(idea.ID); st.Status != store.StatusSettled || st.IssueURL == "" {
		t.Fatalf("after external confirm: %+v", st)
	}
}

// TestExternalLaunchGuards: the offered → launch shortcut exists ONLY for
// external targets — a hive-managed repo still requires acceptance first,
// and its launched body keeps the short footer with no hive pitch.
func TestExternalLaunchGuards(t *testing.T) {
	f := newWave2Server(t, nil)
	idea := f.createIdea(t, "bob-session", "Hive-bound idea", "kubernetes marketplace body", "public")

	// Offer to a registry repo, then try to launch while merely offered.
	rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": "kubestellar/dibs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("hive offer: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/launch", "bob-session", map[string]string{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("launching an offered hive-target idea: want 400, got %d %s", rec.Code, rec.Body.String())
	}

	// Accept, launch — hive footer only, no growth pitch.
	rec = doJSON(t, f.h, "POST", "/api/repos/kubestellar/dibs/decide", "alice-session",
		map[string]string{"ideaID": idea.ID, "decision": "accept"})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/launch", "bob-session", map[string]string{})
	if rec.Code != http.StatusOK {
		t.Fatalf("hive launch: %d %s", rec.Code, rec.Body.String())
	}
	launch := decode[launchResp](t, rec)
	if !strings.HasSuffix(launch.FullBody, settle.Footer) || strings.Contains(launch.FullBody, "Request a hive") {
		t.Fatalf("hive launch body must keep the short footer, no pitch:\n%s", launch.FullBody)
	}

	// A registry repo that is NOT accepting ideas stays rejected — it never
	// falls through to the external path.
	idea2 := f.createIdea(t, "bob-session", "Second idea", "another body", "public")
	rec = doJSON(t, f.h, "PUT", "/api/repos/org/other", "charlie-session", map[string]any{"acceptingIdeas": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle off: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea2.ID+"/offer", "bob-session",
		map[string]string{"repoID": "org/other"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not accepting") {
		t.Fatalf("offer to non-accepting registry repo: want 400 not-accepting, got %d %s", rec.Code, rec.Body.String())
	}
}
