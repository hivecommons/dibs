package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestGuidedFlowUIStrings pins the guided-listing UI: the ever-present
// "＋ List an idea" CTA, the 3-step wizard stepper (Write → ✨ Embellish →
// 🎯 Match), the signed-in market desk zones, the explicit per-idea match
// action, and the honest empty-registry message. The same static page is
// served signed in and signed out (auth is decided client-side), so both
// must carry them.
func TestGuidedFlowUIStrings(t *testing.T) {
	h := newTestServer(t, "/")
	for _, session := range []string{"", "alice-session"} {
		rec := doJSON(t, h, "GET", "/", session, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("UI page (session=%q): %d", session, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			// 1. the ever-present primary CTA + signed-out interstitial
			`id="nav-list-cta"`,
			"＋ List an idea",
			"Sign in to list an idea",
			// 2. the 3-step wizard stepper
			`id="wiz-stepper"`,
			"Refining…",
			"Refinement unavailable. Continue with your draft.",
			// 3. the desk (a stacked section since the one-page collapse) zones
			`id="sec-desk"`,
			"Your desk",
			"My ideas",
			">Matches<span",
			`id="zone-activity"`,
			// 4. the explicit per-idea match action
			"Find repos for this idea",
			// step-3 honest empty state
			"No repos listed yet. Matches appear as repos join.",
			// external (non-hive) targeting in step 3
			"Send to any repo",
			"hive-managed · agent capacity",
			"implementation is not guaranteed",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("UI page (session=%q) missing %q", session, want)
			}
		}
	}
}
