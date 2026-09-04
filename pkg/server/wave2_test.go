package server

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/api"
	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/catalog"
	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

// wave2Fixture is a full server with the fallback matcher, a fake GitHub,
// and notifications. Repos: alice owns kubestellar/dibs, charlie owns
// org/other — both accepting.
type wave2Fixture struct {
	h      http.Handler
	store  *store.Store
	github *settle.Fake
	notify *notify.Store
}

func newWave2Server(t *testing.T, github settle.Client, cncfProjects ...catalog.Project) *wave2Fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "kubestellar/dibs", HiveID: "hive-ks", Owner: "alice", AcceptingIdeas: true,
			Description: "marketplace of kubernetes ideas", Topics: []string{"kubernetes", "marketplace"}},
		{RepoID: "org/other", HiveID: "hive-o", Owner: "charlie", AcceptingIdeas: true,
			Description: "unrelated widgets", Topics: []string{"widgets"}},
	}); err != nil {
		t.Fatalf("registry.Merge: %v", err)
	}
	var cncfCatalog *catalog.Store
	if len(cncfProjects) > 0 {
		raw, err := json.Marshal(cncfProjects)
		if err != nil {
			t.Fatalf("marshal CNCF catalog: %v", err)
		}
		if err := os.WriteFile(dir+"/"+catalog.CacheFile, raw, 0o644); err != nil {
			t.Fatalf("write CNCF catalog: %v", err)
		}
		cncfCatalog, err = catalog.New(dir, "")
		if err != nil {
			t.Fatalf("catalog.New: %v", err)
		}
	}
	nt, err := notify.New(dir)
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	fake, _ := github.(*settle.Fake)
	f := &wave2Fixture{store: st, github: fake, notify: nt}
	f.h = New(Config{
		BasePath: "/",
		HubURL:   "https://hive.kubestellar.io",
		Hub: &auth.FakeHub{Sessions: map[string]auth.Identity{
			"alice-session":   {Username: "alice", DisplayName: "Alice A"},
			"bob-session":     {Username: "bob", DisplayName: "Bob B", AvatarURL: "https://avatars.example/bob.png"},
			"charlie-session": {Username: "charlie", DisplayName: "Charlie C"},
			"oidc-session":    {Username: "okta:00u123", DisplayName: "OIDC User", AvatarURL: "https://avatars.example/oidc.png"},
		}},
		Store:   st,
		Repos:   reg,
		Engine:  &match.Engine{Store: st, Registry: reg, Catalog: cncfCatalog, Notifier: &api.MatchNotifier{Notify: nt}},
		Settler: &settle.Settler{GitHub: github},
		Notify:  nt,
		Version: "test-hash",
	})
	return f
}

type feedResp struct {
	Offers []struct {
		Idea  store.Idea `json:"idea"`
		Score float64    `json:"score"`
	} `json:"offers"`
	Candidates []struct {
		Idea  store.Idea `json:"idea"`
		Score float64    `json:"score"`
	} `json:"candidates"`
}

func (f *wave2Fixture) createIdea(t *testing.T, session, title, body, visibility string) store.Idea {
	t.Helper()
	rec := doJSON(t, f.h, "POST", "/api/ideas", session,
		map[string]string{"title": title, "body": body, "visibility": visibility})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create idea: %d %s", rec.Code, rec.Body.String())
	}
	return decode[store.Idea](t, rec)
}

func (f *wave2Fixture) feed(t *testing.T, session, repoID string) feedResp {
	t.Helper()
	rec := doJSON(t, f.h, "GET", "/api/repos/"+repoID+"/feed", session, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed %s: %d %s", repoID, rec.Code, rec.Body.String())
	}
	return decode[feedResp](t, rec)
}

func (f *wave2Fixture) feedContains(fr feedResp, id string) (inOffers, inCandidates bool) {
	for _, o := range fr.Offers {
		if o.Idea.ID == id {
			inOffers = true
		}
	}
	for _, c := range fr.Candidates {
		if c.Idea.ID == id {
			inCandidates = true
		}
	}
	return
}

// TestPrivateIdeaNeverSurfacesBeforeOffer is THE Wave-2 invariant: a private
// idea must not appear in any feed, match list, or API response to anyone
// but its author until the author explicitly offers it — and then only to
// the offered repo's owner.
func TestPrivateIdeaNeverSurfacesBeforeOffer(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})
	// Bob's private idea, deliberately keyword-loaded to match alice's repo.
	idea := f.createIdea(t, "bob-session", "Kubernetes marketplace idea",
		"A kubernetes marketplace of ideas for kubestellar dibs.", "private")

	// 1. Repo owner feeds: nowhere.
	fr := f.feed(t, "alice-session", "kubestellar/dibs")
	if o, c := f.feedContains(fr, idea.ID); o || c {
		t.Fatalf("PRIVATE IDEA LEAKED into alice's feed before offer: offers=%v candidates=%v", o, c)
	}
	if len(fr.Candidates) != 0 || len(fr.Offers) != 0 {
		t.Fatalf("feed should be empty, got %+v", fr)
	}

	// 2. Direct decide (accept/decline/pass) by a repo owner: 404 — even
	// existence must not leak.
	for _, decision := range []string{"accept", "decline", "pass"} {
		rec := doJSON(t, f.h, "POST", "/api/repos/kubestellar/dibs/decide", "alice-session",
			map[string]string{"ideaID": idea.ID, "decision": decision})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("decide %s on unoffered private idea: want 404, got %d %s", decision, rec.Code, rec.Body.String())
		}
	}

	// 3. Direct fetch and matches: 404 for non-authors.
	if rec := doJSON(t, f.h, "GET", "/api/ideas/"+idea.ID, "alice-session", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("fetch private idea: want 404, got %d", rec.Code)
	}
	if rec := doJSON(t, f.h, "GET", "/api/ideas/"+idea.ID+"/matches", "alice-session", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("matches of private idea: want 404, got %d", rec.Code)
	}

	// 4. Public browse: absent.
	rec := doJSON(t, f.h, "GET", "/api/ideas?scope=public", "alice-session", nil)
	if l := decode[[]store.Idea](t, rec); len(l) != 0 {
		t.Fatalf("private idea in public browse: %+v", l)
	}

	// 5. Bob offers it to alice's repo — the explicit reveal.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": "kubestellar/dibs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("offer: %d %s", rec.Code, rec.Body.String())
	}

	// Now — and only now — alice sees it, as an OFFER (never a candidate).
	fr = f.feed(t, "alice-session", "kubestellar/dibs")
	if o, c := f.feedContains(fr, idea.ID); !o || c {
		t.Fatalf("after offer: offers=%v candidates=%v (want offer only)", o, c)
	}
	// Alice can read it directly now.
	if rec := doJSON(t, f.h, "GET", "/api/ideas/"+idea.ID, "alice-session", nil); rec.Code != http.StatusOK {
		t.Fatalf("fetch offered private idea as target owner: %d", rec.Code)
	}

	// 6. Charlie (owner of a DIFFERENT repo) still sees nothing, anywhere.
	fr = f.feed(t, "charlie-session", "org/other")
	if o, c := f.feedContains(fr, idea.ID); o || c {
		t.Fatal("PRIVATE IDEA LEAKED to a repo it was never offered to")
	}
	if rec := doJSON(t, f.h, "GET", "/api/ideas/"+idea.ID, "charlie-session", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("charlie fetching private idea: want 404, got %d", rec.Code)
	}
	rec = doJSON(t, f.h, "POST", "/api/repos/org/other/decide", "charlie-session",
		map[string]string{"ideaID": idea.ID, "decision": "accept"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("charlie accepting private idea offered elsewhere: want 404, got %d %s", rec.Code, rec.Body.String())
	}
	// Public browse still clean.
	rec = doJSON(t, f.h, "GET", "/api/ideas?scope=public", "charlie-session", nil)
	if l := decode[[]store.Idea](t, rec); len(l) != 0 {
		t.Fatalf("offered private idea leaked into public browse: %+v", l)
	}
}

// TestOfferAcceptSettleFlow: the LEGACY happy path end to end (a GitHub
// client is configured, so accept still opens the credited issue
// server-side) — match, offer, accept, credited issue, notifications.
func TestOfferAcceptSettleFlow(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})
	idea := f.createIdea(t, "bob-session", "Kubernetes marketplace boost",
		"Improve the kubernetes marketplace matching for dibs.", "public")

	// Ideator side: matches include alice's repo (fallback scorer).
	rec := doJSON(t, f.h, "GET", "/api/ideas/"+idea.ID+"/matches", "bob-session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("matches: %d %s", rec.Code, rec.Body.String())
	}
	type matchesResp struct {
		TLDR    string `json:"tldr"`
		Matches []struct {
			Repo  registry.RepoProfile `json:"repo"`
			Score float64              `json:"score"`
		} `json:"matches"`
	}
	mres := decode[matchesResp](t, rec)
	if mres.TLDR == "" {
		t.Fatal("expected a cached TLDR")
	}
	var found bool
	for _, m := range mres.Matches {
		if m.Repo.RepoID == "kubestellar/dibs" && m.Score > 0 {
			found = true
		}
	}

	if !found {
		t.Fatalf("expected a scored match with kubestellar/dibs: %+v", mres.Matches)
	}

	// Offer → status offered, alice notified.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": "kubestellar/dibs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("offer: %d %s", rec.Code, rec.Body.String())
	}
	if got := decode[store.Idea](t, rec); got.Status != store.StatusOffered || got.OfferTo("kubestellar/dibs") == nil {
		t.Fatalf("after offer: %+v", got)
	}
	// Double-offer rejected.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": "kubestellar/dibs"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("double offer: want 400, got %d", rec.Code)
	}

	// Alice accepts → settled with a credited issue.
	rec = doJSON(t, f.h, "POST", "/api/repos/kubestellar/dibs/decide", "alice-session",
		map[string]string{"ideaID": idea.ID, "decision": "accept"})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	res := decode[map[string]any](t, rec)
	if res["result"] != "settled" || res["issueURL"] == "" {
		t.Fatalf("accept result: %+v", res)
	}
	if len(f.github.Issues) != 1 {
		t.Fatalf("github issues: %+v", f.github.Issues)
	}
	issue := f.github.Issues[0]
	if issue.RepoID != "kubestellar/dibs" || !strings.Contains(issue.Body, "@bob") ||
		!strings.Contains(issue.Body, "Bob B") || len(issue.Labels) != 1 || issue.Labels[0] != settle.Label {
		t.Fatalf("credited issue wrong: %+v", issue)
	}
	final, err := f.store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if final.Status != store.StatusSettled || final.IssueURL == "" || final.TargetRepo != "kubestellar/dibs" {
		t.Fatalf("final idea: %+v", final)
	}

	// Notifications: alice got the offer; bob got accepted + issue.
	aliceKinds := kinds(f.notify.ListByUser("alice", false))
	if !aliceKinds[notify.KindOffer] {
		t.Fatalf("alice notifications: %+v", f.notify.ListByUser("alice", false))
	}
	bobKinds := kinds(f.notify.ListByUser("bob", false))
	if !bobKinds[notify.KindAccepted] || !bobKinds[notify.KindIssue] {
		t.Fatalf("bob notifications: %+v", f.notify.ListByUser("bob", false))
	}
}

func kinds(ns []notify.Notification) map[string]bool {
	out := map[string]bool{}
	for _, n := range ns {
		out[n.Kind] = true
	}
	return out
}

// TestDeclineAndReoffer: decline moves the offer and idea to declined,
// notifies the ideator, and a re-offer is allowed.
func TestDeclineAndReoffer(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})
	idea := f.createIdea(t, "bob-session", "Meh idea", "Body of the meh idea.", "public")
	rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": "kubestellar/dibs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("offer: %d", rec.Code)
	}
	rec = doJSON(t, f.h, "POST", "/api/repos/kubestellar/dibs/decide", "alice-session",
		map[string]string{"ideaID": idea.ID, "decision": "decline"})
	if rec.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := f.store.Get(idea.ID)
	if got.Status != store.StatusDeclined || got.OfferTo("kubestellar/dibs").Status != store.OfferDeclined {
		t.Fatalf("after decline: %+v", got)
	}
	if !kinds(f.notify.ListByUser("bob", false))[notify.KindDeclined] {
		t.Fatal("bob missing the declined notification")
	}
	// No pending offer → nothing settles on GitHub.
	if len(f.github.Issues) != 0 {
		t.Fatalf("no issue should exist: %+v", f.github.Issues)
	}
	// Re-offer after decline is legal (declined → offered).
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": "kubestellar/dibs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("re-offer: %d %s", rec.Code, rec.Body.String())
	}
	got, _ = f.store.Get(idea.ID)
	if got.Status != store.StatusOffered || got.OfferTo("kubestellar/dibs").Status != store.OfferPending {
		t.Fatalf("after re-offer: %+v", got)
	}
}

func TestIdeaMatchesReturnOneHiveThenTwoNonHiveCNCF(t *testing.T) {
	f := newWave2Server(t, nil,
		catalog.Project{Name: "Hive duplicate", RepoID: "kubestellar/dibs", RepoURL: "https://github.com/hivecommons/dibs", Maturity: "sandbox", Description: "kubernetes marketplace matching"},
		catalog.Project{Name: "Istio", RepoID: "istio/istio", RepoURL: "https://github.com/istio/istio", Maturity: "graduated", Category: "Service Proxy", Description: "kubernetes service mesh proxy matching"},
		catalog.Project{Name: "Envoy", RepoID: "envoyproxy/envoy", RepoURL: "https://github.com/envoyproxy/envoy", Maturity: "graduated", Category: "Service Proxy", Description: "kubernetes service proxy matching"},
		catalog.Project{Name: "Vitess", RepoID: "vitessio/vitess", RepoURL: "https://github.com/vitessio/vitess", Maturity: "graduated", Category: "Database", Description: "kubernetes database matching"},
	)
	idea := f.createIdea(t, "bob-session", "Kubernetes marketplace matching",
		"Improve kubernetes service proxy matching.", "public")

	rec := doJSON(t, f.h, "GET", "/api/ideas/"+idea.ID+"/matches", "bob-session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("matches: %d %s", rec.Code, rec.Body.String())
	}
	var mres struct {
		Matches []struct {
			Repo registry.RepoProfile `json:"repo"`
		} `json:"matches"`
		CNCF []match.CNCFMatch `json:"cncf"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&mres); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(mres.Matches) != 1 {
		t.Fatalf("hive matches = %d, want 1: %+v", len(mres.Matches), mres.Matches)
	}
	if mres.Matches[0].Repo.RepoID != "kubestellar/dibs" {
		t.Fatalf("top hive match = %+v", mres.Matches[0].Repo)
	}
	if len(mres.CNCF) != 2 {
		t.Fatalf("cncf matches = %d, want 2: %+v", len(mres.CNCF), mres.CNCF)
	}
	for _, m := range mres.CNCF {
		if m.RepoID == "kubestellar/dibs" {
			t.Fatalf("hive-managed repo leaked into CNCF section: %+v", mres.CNCF)
		}
	}
}

func TestAdminRematchDryApplyAndPayload(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "alice")
	f := newWave2Server(t, nil,
		catalog.Project{Name: "Envoy", RepoID: "envoyproxy/envoy", RepoURL: "https://github.com/envoyproxy/envoy", Maturity: "graduated", Category: "Service Proxy", Description: "kubernetes marketplace matching proxy"},
		catalog.Project{Name: "Istio", RepoID: "istio/istio", RepoURL: "https://github.com/istio/istio", Maturity: "graduated", Category: "Service Mesh", Description: "kubernetes service mesh marketplace"},
		catalog.Project{Name: "Vitess", RepoID: "vitessio/vitess", RepoURL: "https://github.com/vitessio/vitess", Maturity: "graduated", Category: "Database", Description: "database clustering"},
	)
	idea := f.createIdea(t, "bob-session", "Kubernetes marketplace matching",
		"kubernetes marketplace matching for open source repos", "public")
	waitDone := func(id string) {
		t.Helper()
		for i := 0; i < 50; i++ {
			rec := doJSON(t, f.h, "GET", "/api/admin/ideas/"+id+"/rematch", "alice-session", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("poll rematch %s: %d %s", id, rec.Code, rec.Body.String())
			}
			var st struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
				t.Fatalf("decode rematch %s: %v", id, err)
			}
			if st.Status == "done" {
				return
			}
			if st.Status == "error" {
				t.Fatalf("rematch %s errored", id)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("rematch %s did not finish", id)
	}

	rec := doJSON(t, f.h, "POST", "/api/admin/ideas/"+idea.ID+"/rematch?dry=1", "bob-session", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin dry rematch: want 403, got %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "GET", "/api/admin/ideas/"+idea.ID+"/rematch", "bob-session", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin poll rematch: want 403, got %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, f.h, "POST", "/api/admin/ideas/"+idea.ID+"/rematch?dry=1", "alice-session", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("admin dry rematch start: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/admin/ideas/"+idea.ID+"/rematch", "alice-session", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("double-start rematch: want 409, got %d %s", rec.Code, rec.Body.String())
	}
	var dry struct {
		JobID  string `json:"jobID"`
		Status string `json:"status"`
		Dry    bool   `json:"dry"`
		Events []struct {
			Seq    int64   `json:"seq"`
			Phase  string  `json:"phase"`
			RepoID string  `json:"repoID"`
			Score  float64 `json:"score"`
		} `json:"events"`
		Next    int64 `json:"next"`
		Matches struct {
			Count int               `json:"count"`
			Hive  []store.Match     `json:"hive"`
			CNCF  []store.CNCFMatch `json:"cncf"`
		} `json:"matches"`
	}
	for i := 0; i < 50; i++ {
		rec = doJSON(t, f.h, "GET", "/api/admin/ideas/"+idea.ID+"/rematch", "alice-session", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("poll dry rematch: %d %s", rec.Code, rec.Body.String())
		}
		if err := json.NewDecoder(rec.Body).Decode(&dry); err != nil {
			t.Fatalf("decode dry: %v", err)
		}
		if dry.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if dry.Status != "done" || !dry.Dry || len(dry.Matches.Hive) != 2 || len(dry.Matches.CNCF) != 2 {
		t.Fatalf("dry rematch missing output: %+v", dry)
	}
	if dry.JobID == "" {
		t.Fatalf("dry rematch missing jobID: %+v", dry)
	}
	if len(dry.Events) == 0 || dry.Next == 0 {
		t.Fatalf("dry rematch missing progress events: %+v", dry)
	}
	for i, ev := range dry.Events {
		if ev.Seq <= 0 || ev.Phase == "" {
			t.Fatalf("bad progress event %d: %+v", i, ev)
		}
		if i > 0 && ev.Seq <= dry.Events[i-1].Seq {
			t.Fatalf("progress seqs not increasing: %+v", dry.Events)
		}
	}
	rec = doJSON(t, f.h, "GET", "/api/admin/ideas/"+idea.ID+"/rematch?since="+strconv.FormatInt(dry.Events[0].Seq, 10), "alice-session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll dry rematch since: %d %s", rec.Code, rec.Body.String())
	}
	var sliced struct {
		Events []struct {
			Seq int64 `json:"seq"`
		} `json:"events"`
		Next int64 `json:"next"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&sliced); err != nil {
		t.Fatalf("decode sliced: %v", err)
	}
	if len(sliced.Events) != len(dry.Events)-1 || sliced.Next != dry.Next {
		t.Fatalf("since slicing mismatch: got %+v from %+v", sliced, dry.Events)
	}
	got, _ := f.store.Get(idea.ID)
	if len(got.Matches) != 0 || len(got.CNCFMatches) != 0 {
		t.Fatalf("dry rematch persisted suggestions: %+v", got)
	}

	rec = doJSON(t, f.h, "POST", "/api/admin/ideas/"+idea.ID+"/rematch", "alice-session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin apply dry results: %d %s", rec.Code, rec.Body.String())
	}
	var appliedDry struct {
		Status  string `json:"status"`
		Dry     bool   `json:"dry"`
		Matches struct {
			Hive []store.Match     `json:"hive"`
			CNCF []store.CNCFMatch `json:"cncf"`
		} `json:"matches"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&appliedDry); err != nil {
		t.Fatalf("decode applied dry: %v", err)
	}
	if appliedDry.Status != "done" || appliedDry.Dry || !reflect.DeepEqual(appliedDry.Matches.Hive, dry.Matches.Hive) ||
		!reflect.DeepEqual(appliedDry.Matches.CNCF, dry.Matches.CNCF) {
		t.Fatalf("apply did not reuse dry results: got %+v want %+v", appliedDry, dry.Matches)
	}
	got, _ = f.store.Get(idea.ID)
	if len(got.Matches) != 2 || len(got.CNCFMatches) != 2 || got.MatchesUpdatedAt.IsZero() {
		t.Fatalf("apply did not persist suggestions: %+v", got)
	}
	rec = doJSON(t, f.h, "POST", "/api/admin/ideas/"+idea.ID+"/rematch", "alice-session", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("repeat apply without fresh dry: want 409, got %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, f.h, "GET", "/api/admin/ideas", "alice-session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin ideas: %d %s", rec.Code, rec.Body.String())
	}
	var ideas []struct {
		ID      string `json:"id"`
		Matches struct {
			Count int               `json:"count"`
			Hive  []store.Match     `json:"hive"`
			CNCF  []store.CNCFMatch `json:"cncf"`
		} `json:"matches"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&ideas); err != nil {
		t.Fatalf("decode admin ideas: %v", err)
	}
	if len(ideas) != 1 || ideas[0].ID != idea.ID || len(ideas[0].Matches.Hive) != 2 || len(ideas[0].Matches.CNCF) != 2 {
		t.Fatalf("admin payload missing stored matches: %+v", ideas)
	}

	fresh := f.createIdea(t, "bob-session", "Fresh apply", "kubernetes marketplace matching", "public")
	rec = doJSON(t, f.h, "POST", "/api/admin/ideas/"+fresh.ID+"/rematch", "alice-session", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("apply without dry should be gated: got %d %s", rec.Code, rec.Body.String())
	}

	stale := f.createIdea(t, "bob-session", "Stale dry", "kubernetes marketplace matching", "public")
	rec = doJSON(t, f.h, "POST", "/api/admin/ideas/"+stale.ID+"/rematch?dry=1", "alice-session", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("stale dry start: %d %s", rec.Code, rec.Body.String())
	}
	waitDone(stale.ID)
	rec = doJSON(t, f.h, "PUT", "/api/ideas/"+stale.ID, "bob-session",
		map[string]string{"title": "Stale dry edited", "body": stale.Body, "visibility": "public", "status": "draft"})
	if rec.Code != http.StatusOK {
		t.Fatalf("edit stale idea: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/admin/ideas/"+stale.ID+"/rematch", "alice-session", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale dry apply should be gated: got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminAuthorIdentityPayload(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "alice")
	f := newWave2Server(t, nil)
	githubIdea := f.createIdea(t, "bob-session", "GitHub idea", "body", "public")
	oidcIdea := f.createIdea(t, "oidc-session", "OIDC idea", "body", "public")
	if _, err := f.store.Mutate(githubIdea.ID, false, func(i *store.Idea) error {
		i.AuthorProvider = ""
		return nil
	}); err != nil {
		t.Fatalf("clear legacy provider: %v", err)
	}

	rec := doJSON(t, f.h, "GET", "/api/admin/ideas", "alice-session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin ideas: %d %s", rec.Code, rec.Body.String())
	}
	var ideas []struct {
		ID             string `json:"id"`
		Author         string `json:"author"`
		AuthorAvatar   string `json:"authorAvatar"`
		AuthorProvider string `json:"authorProvider"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&ideas); err != nil {
		t.Fatalf("decode admin ideas: %v", err)
	}
	byID := map[string]struct {
		Author         string `json:"author"`
		AuthorAvatar   string `json:"authorAvatar"`
		AuthorProvider string `json:"authorProvider"`
	}{}
	for _, idea := range ideas {
		byID[idea.ID] = struct {
			Author         string `json:"author"`
			AuthorAvatar   string `json:"authorAvatar"`
			AuthorProvider string `json:"authorProvider"`
		}{idea.Author, idea.AuthorAvatar, idea.AuthorProvider}
	}
	if got := byID[githubIdea.ID]; got.AuthorProvider != "github" || got.AuthorAvatar != "https://avatars.example/bob.png" {
		t.Fatalf("github author identity = %+v", got)
	}
	if got := byID[oidcIdea.ID]; got.Author != "okta:00u123" || got.AuthorProvider != "okta" || got.AuthorAvatar != "https://avatars.example/oidc.png" {
		t.Fatalf("oidc author identity = %+v", got)
	}
}

// TestRepoFeedCandidatesAndPass: public ideas appear as candidates for repo
// owners; pass hides them for that repo only; ideator pass hides repos.
func TestRepoFeedCandidatesAndPass(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})
	idea := f.createIdea(t, "bob-session", "Kubernetes widget marketplace",
		"kubernetes marketplace widgets for everyone", "public")

	fr := f.feed(t, "alice-session", "kubestellar/dibs")
	if _, c := f.feedContains(fr, idea.ID); !c {
		t.Fatalf("public idea missing from candidates: %+v", fr)
	}
	// Non-owner cannot read a repo's feed or decide for it.
	if rec := doJSON(t, f.h, "GET", "/api/repos/kubestellar/dibs/feed", "bob-session", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner feed: want 403, got %d", rec.Code)
	}
	rec := doJSON(t, f.h, "POST", "/api/repos/kubestellar/dibs/decide", "bob-session",
		map[string]string{"ideaID": idea.ID, "decision": "pass"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner decide: want 403, got %d", rec.Code)
	}

	// Owner passes → gone from this repo's candidates, still in charlie's.
	rec = doJSON(t, f.h, "POST", "/api/repos/kubestellar/dibs/decide", "alice-session",
		map[string]string{"ideaID": idea.ID, "decision": "pass"})
	if rec.Code != http.StatusOK {
		t.Fatalf("pass: %d %s", rec.Code, rec.Body.String())
	}
	fr = f.feed(t, "alice-session", "kubestellar/dibs")
	if _, c := f.feedContains(fr, idea.ID); c {
		t.Fatal("passed idea resurfaced")
	}
	fr = f.feed(t, "charlie-session", "org/other")
	if _, c := f.feedContains(fr, idea.ID); !c {
		t.Fatal("pass must be scoped to one repo")
	}

	// Ideator pass: repo disappears from the idea's matches.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/pass", "bob-session",
		map[string]string{"repoID": "org/other"})
	if rec.Code != http.StatusOK {
		t.Fatalf("ideator pass: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "GET", "/api/ideas/"+idea.ID+"/matches", "bob-session", nil)
	body := rec.Body.String()
	if strings.Contains(body, "org/other") {
		t.Fatalf("passed repo still in matches: %s", body)
	}
}

// TestPublicDirectAccept: a repo owner can accept a public candidate that
// was never offered (draft → accepted → settled, legacy client configured).
func TestPublicDirectAccept(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})
	idea := f.createIdea(t, "bob-session", "Take me", "kubernetes marketplace body", "public")
	rec := doJSON(t, f.h, "POST", "/api/repos/kubestellar/dibs/decide", "alice-session",
		map[string]string{"ideaID": idea.ID, "decision": "accept"})
	if rec.Code != http.StatusOK {
		t.Fatalf("direct accept: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := f.store.Get(idea.ID)
	if got.Status != store.StatusSettled || got.IssueURL == "" {
		t.Fatalf("after direct accept: %+v", got)
	}
	// A settled idea cannot be accepted again.
	rec = doJSON(t, f.h, "POST", "/api/repos/org/other/decide", "charlie-session",
		map[string]string{"ideaID": idea.ID, "decision": "accept"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("double accept: want 400, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestAcceptWithoutGitHub: the DEFAULT matchmaker mode (no token) — accept
// records the acceptance and stops; no issue is opened by Dibs, the ideator
// files it themselves (see settlement_test.go).
func TestAcceptWithoutGitHub(t *testing.T) {
	f := newWave2Server(t, nil) // nil Client — DIBS_GITHUB_TOKEN unset
	idea := f.createIdea(t, "bob-session", "Tokenless", "kubernetes marketplace body", "public")
	rec := doJSON(t, f.h, "POST", "/api/repos/kubestellar/dibs/decide", "alice-session",
		map[string]string{"ideaID": idea.ID, "decision": "accept"})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	res := decode[map[string]any](t, rec)
	if res["result"] != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
	got, _ := f.store.Get(idea.ID)
	if got.Status != store.StatusAccepted || got.IssueURL != "" || got.TargetRepo != "kubestellar/dibs" {
		t.Fatalf("after matchmaker accept: %+v", got)
	}
}

// TestNotificationsAPI: bell feed round-trip with mark-as-read.
func TestNotificationsAPI(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})
	idea := f.createIdea(t, "bob-session", "Notify me", "kubernetes marketplace body", "public")
	rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": "kubestellar/dibs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("offer: %d", rec.Code)
	}

	rec = doJSON(t, f.h, "GET", "/api/notifications", "alice-session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("notifications: %d", rec.Code)
	}
	res := decode[struct {
		Notifications []notify.Notification `json:"notifications"`
		Unread        int                   `json:"unread"`
	}](t, rec)
	if res.Unread == 0 || len(res.Notifications) == 0 {
		t.Fatalf("alice should have the offer notification: %+v", res)
	}

	rec = doJSON(t, f.h, "POST", "/api/notifications/read", "alice-session", map[string]any{"all": true})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("mark read: %d", rec.Code)
	}
	rec = doJSON(t, f.h, "GET", "/api/notifications", "alice-session", nil)
	res = decode[struct {
		Notifications []notify.Notification `json:"notifications"`
		Unread        int                   `json:"unread"`
	}](t, rec)
	if res.Unread != 0 {
		t.Fatalf("unread after mark-all-read: %+v", res)
	}
}
