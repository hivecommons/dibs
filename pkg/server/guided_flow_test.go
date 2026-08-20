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
			"Ready to list your idea?",
			// 2. the 3-step wizard stepper
			`id="wiz-stepper"`,
			"Polishing your idea with AI",
			"AI polish unavailable — your draft is ready",
			// 3. the desk (its own view since the terminal redesign) zones
			`id="view-desk"`,
			"Your desk",
			"My ideas",
			"Matches to review",
			`id="zone-activity"`,
			// 4. the explicit per-idea match action
			"Find repos for this idea",
			// step-3 honest empty state
			"No repos are listed yet — your idea is on the market; matches appear as repos join.",
			// external (non-hive) targeting in step 3
			"…or send it to any repo",
			"hive-powered — agent capacity ready",
			"not hive-managed — implementation not guaranteed… yet",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("UI page (session=%q) missing %q", session, want)
			}
		}
	}
}
