package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// repoPathReq builds a request carrying the {org}/{repo} path values the
// pkg/server mux would set for the public repo endpoints.
func repoPathReq(target, org, repo string) *http.Request {
	req := httptest.NewRequest("GET", target, nil)
	req.SetPathValue("org", org)
	req.SetPathValue("repo", repo)
	return req
}

func TestHandleRepoIndexUnknownRepo(t *testing.T) {
	a, _ := newAPIFixture(t)
	rec := httptest.NewRecorder()
	a.HandleRepoIndex(rec, repoPathReq("/api/repos/no/pe/index", "no", "pe"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRepoIndexChartPayload(t *testing.T) {
	now := time.Date(2026, 9, 25, 15, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, _ := newAPIFixture(t)

	settled := mustCreate(t, a, "alice", "Shipped idea", store.VisibilityPublic, store.StatusSettled)
	if _, err := a.Store.Mutate(settled.ID, false, func(i *store.Idea) error {
		i.TargetRepo = "kubestellar/dibs"
		i.UpdatedAt = now.Add(-2 * time.Hour)
		return nil
	}); err != nil {
		t.Fatalf("seed settled idea: %v", err)
	}

	rec := httptest.NewRecorder()
	a.HandleRepoIndex(rec, repoPathReq("/api/repos/kubestellar/dibs/index", "kubestellar", "dibs"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[RepoIndex](t, rec)
	if got.RepoID != "kubestellar/dibs" {
		t.Errorf("RepoID=%q", got.RepoID)
	}
	if got.Symbol == "" {
		t.Errorf("Symbol is empty")
	}
	if len(got.Points) != indexDays || len(got.Bars) != indexDays {
		t.Fatalf("points=%d bars=%d, want %d each", len(got.Points), len(got.Bars), indexDays)
	}
	// One idea filed today (+8) over base 100, trailing 3-day mean:
	// current = round1((100+100+108)/3) = 102.7, previous day = 100.0.
	if got.Current != 102.7 {
		t.Errorf("Current=%v, want 102.7", got.Current)
	}
	if got.Delta != 2.7 {
		t.Errorf("Delta=%v, want 2.7", got.Delta)
	}
	last := got.Bars[indexDays-1]
	if last.IssuesHuman != 1 || last.PRsClanker != 0 {
		t.Errorf("last bar = %+v, want IssuesHuman=1 PRsClanker=0", last)
	}
	if last.T != "2026-09-25" {
		t.Errorf("last bar date = %q", last.T)
	}
}

// TestHandleRepoIndexMixedCaseURL pins that a differently-cased path resolves
// to the registered spelling, so the symbol and idea events still apply.
func TestHandleRepoIndexMixedCaseURL(t *testing.T) {
	now := time.Date(2026, 9, 25, 15, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, _ := newAPIFixture(t)

	settled := mustCreate(t, a, "alice", "Shipped idea", store.VisibilityPublic, store.StatusSettled)
	if _, err := a.Store.Mutate(settled.ID, false, func(i *store.Idea) error {
		i.TargetRepo = "kubestellar/dibs"
		i.UpdatedAt = now.Add(-2 * time.Hour)
		return nil
	}); err != nil {
		t.Fatalf("seed settled idea: %v", err)
	}

	rec := httptest.NewRecorder()
	a.HandleRepoIndex(rec, repoPathReq("/api/repos/KubeStellar/Dibs/index", "KubeStellar", "Dibs"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[RepoIndex](t, rec)
	if got.RepoID != "kubestellar/dibs" {
		t.Errorf("RepoID=%q, want registered spelling", got.RepoID)
	}
	if got.Symbol == "" {
		t.Errorf("Symbol is empty")
	}
	if got.Current != 102.7 {
		t.Errorf("Current=%v, want 102.7", got.Current)
	}
}

func TestHandleRepoQRUnknownRepo(t *testing.T) {
	a, _ := newAPIFixture(t)
	rec := httptest.NewRecorder()
	a.HandleRepoQR(rec, repoPathReq("/api/repos/no/pe/qr.png", "no", "pe"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func TestHandleRepoQRServesPNG(t *testing.T) {
	a, _ := newAPIFixture(t)
	if err := a.Registry.Merge([]registry.RepoProfile{
		{RepoID: "org/linked", HiveID: "hive-l", Owner: "erin",
			ContributeURL: "https://example.test/hives/linked/contribute"},
	}); err != nil {
		t.Fatalf("registry.Merge: %v", err)
	}

	fetch := func(org, repo string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		a.HandleRepoQR(rec, repoPathReq("/api/repos/"+org+"/"+repo+"/qr.png", org, repo))
		if rec.Code != http.StatusOK {
			t.Fatalf("qr %s/%s status=%d body=%s", org, repo, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Errorf("qr %s/%s Content-Type=%q", org, repo, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "max-age=3600" {
			t.Errorf("qr %s/%s Cache-Control=%q", org, repo, cc)
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), pngMagic) {
			t.Errorf("qr %s/%s body is not a PNG", org, repo)
		}
		return rec
	}

	// kubestellar/dibs has no ContributeURL -> encodes the default target;
	// org/linked encodes its own URL, so the two codes must differ.
	defaultQR := fetch("kubestellar", "dibs")
	linkedQR := fetch("org", "linked")
	if bytes.Equal(defaultQR.Body.Bytes(), linkedQR.Body.Bytes()) {
		t.Errorf("QR for a repo with its own ContributeURL matches the default-URL QR")
	}
}

func TestRound1(t *testing.T) {
	tests := []struct{ in, want float64 }{
		{0, 0},
		{2.34, 2.3},
		{2.35, 2.4},
		{-2.34, -2.3},
		{-2.35, -2.4},
		{-0.04, -0.0},
	}
	for _, tt := range tests {
		if got := round1(tt.in); got != tt.want {
			t.Errorf("round1(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIdeaCoverageCovers(t *testing.T) {
	fetched := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	t.Run("nil coverage never covers", func(t *testing.T) {
		var c *ideaCoverage
		if c.covers("2026-09-25", fetched) {
			t.Fatal("nil coverage covered an event")
		}
	})

	t.Run("exhausted total never covers", func(t *testing.T) {
		c := &ideaCoverage{byDate: map[string]int{"2026-09-25": 1}, total: 0, fetchedAt: fetched}
		if c.covers("2026-09-25", fetched) {
			t.Fatal("zero-total coverage covered an event")
		}
	})

	t.Run("events after fetchedAt are never covered", func(t *testing.T) {
		c := &ideaCoverage{byDate: map[string]int{"2026-09-25": 1}, total: 1, fetchedAt: fetched}
		if c.covers("2026-09-25", fetched.Add(time.Second)) {
			t.Fatal("post-fetch event was covered")
		}
	})

	t.Run("per-date hit decrements", func(t *testing.T) {
		c := &ideaCoverage{byDate: map[string]int{"2026-09-24": 1}, total: 1,
			startDate: "2026-09-20", endDate: "2026-09-25", fetchedAt: fetched}
		if !c.covers("2026-09-24", fetched) {
			t.Fatal("first event on a counted date was not covered")
		}
		if c.total != 0 || c.byDate["2026-09-24"] != 0 {
			t.Fatalf("counts not decremented: total=%d byDate=%v", c.total, c.byDate)
		}
	})

	t.Run("in-range date without a per-date count uses the range", func(t *testing.T) {
		c := &ideaCoverage{byDate: map[string]int{}, total: 2,
			startDate: "2026-09-20", endDate: "2026-09-25", fetchedAt: fetched}
		if !c.covers("2026-09-22", fetched) {
			t.Fatal("in-range event was not covered")
		}
		if c.total != 1 {
			t.Fatalf("total=%d, want 1", c.total)
		}
	})

	t.Run("out-of-range date is not covered", func(t *testing.T) {
		c := &ideaCoverage{byDate: map[string]int{}, total: 1,
			startDate: "2026-09-20", endDate: "2026-09-25", fetchedAt: fetched}
		if c.covers("2026-09-19", fetched) {
			t.Fatal("pre-range event was covered")
		}
		if c.covers("2026-09-26", fetched) {
			t.Fatal("post-range event was covered")
		}
		if c.total != 1 {
			t.Fatalf("total=%d, want 1", c.total)
		}
	})

	t.Run("empty startDate disables the range fallback", func(t *testing.T) {
		c := &ideaCoverage{byDate: map[string]int{}, total: 1, fetchedAt: fetched}
		if c.covers("2026-09-22", fetched) {
			t.Fatal("range fallback fired without a range")
		}
	})
}
