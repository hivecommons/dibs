package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestTerminalHomeForEveryone pins the terminal-redesign invariants: the
// market terminal (tape, chart, stats, live board) is everyone's home page —
// not gated behind the logged-out landing class — and the jump menu replaces
// the persistent nav row. After the one-page collapse, every destination is
// a stacked section on a single scrolling page; flows are modals on top.
func TestTerminalHomeForEveryone(t *testing.T) {
	h := newTestServer(t, "/")
	rec := doJSON(t, h, "GET", "/", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("UI page: %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		// terminal pieces present (and the tape no longer anon-gated)
		`<div id="tape" aria-label=`,
		`class="chart-panel"`,
		`id="mkt-stats"`,
		`id="board-trending"`,
		// one page: every destination is a stacked section
		`id="sec-market"`,
		`id="sec-desk" data-auth`,
		`id="sec-ideas"`,
		`id="sec-repos" data-auth`,
		`id="sec-board"`,
		// flows are modals over the one page
		`id="modal-edit" hidden role="dialog" aria-modal="true"`,
		`id="modal-idea"`,
		`id="modal-match"`,
		`id="modal-launch"`,
		`id="modal-feed"`,
		`data-close-modal`,
		// home carries a slim desk summary strip for signed-in users
		`class="desk-strip" data-auth`,
		"Open desk",
		// jump menu: dialog + combobox/listbox semantics + header affordance
		`id="jump-overlay" hidden role="dialog" aria-modal="true"`,
		`id="jump-input"`,
		`role="combobox"`,
		`id="jump-list" role="listbox"`,
		`id="jump-btn"`,
		// the old persistent nav row is gone
	} {
		if !strings.Contains(body, want) {
			t.Errorf("UI page missing %q", want)
		}
	}
	if strings.Contains(body, `id="tape" class="landing-extra"`) {
		t.Errorf("ticker tape is still gated to the logged-out landing")
	}
	if strings.Contains(body, `<nav aria-label="Main">`) {
		t.Errorf("persistent nav button row should be replaced by the jump menu")
	}
	// The multi-view shell is gone: no swappable section.view panes remain.
	for _, gone := range []string{`id="view-home"`, `id="view-mine"`, `id="view-desk"`, `class="view active"`, `section.view`} {
		if strings.Contains(body, gone) {
			t.Errorf("one-page collapse left old multi-view artifact %q", gone)
		}
	}
}

// TestEmojiStripSerious pins the founder's tone directive: decorative emojis
// are gone from nav, headers, buttons, tabs, and log lines. Gamification
// emojis (medals, badge emojis from the API) and the handshake favicon are
// the only intentional survivors. The copy itself must read like a terse
// financial product: no sparkle language, no cutesy microcopy, no
// exclamation-point enthusiasm.
func TestEmojiStripSerious(t *testing.T) {
	h := newTestServer(t, "/")
	rec := doJSON(t, h, "GET", "/", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("UI page: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, banned := range []string{
		"🔥", "💼", "🌍", "🐝", "📊", "🏦", "🎯", "🚀", "💌", "📬",
		"🤔", "🏛️", "📈", "🔭", "🔔", "🌐", "🔒", "💜", "🕳️", "🎉",
		"⚠️", "📋", "🏷️", "❌", "✅", "🙂", "📝", "🔗", "✍️", "🍯",
		"🌼", "🔸", "🌧️", "💪", "💾", "🏆", "🆕", "✨", "💡", "👑",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("UI page still contains decorative emoji %q", banned)
		}
	}
	// Banned AI-slop copy: sparkle language, cutesy microcopy, hype.
	for _, slop := range []string{
		"Embellish", "Polishing your idea", "It's a match", "Ready to list your idea",
		"Maybe later", "Keep going", "First Dibs badge", "heartbreak",
		"points!", "welcome to the leaderboard", "Top of the hive",
		"the throne is empty", "warming up", "sells the idea",
	} {
		if strings.Contains(body, slop) {
			t.Errorf("UI page still contains AI-slop copy %q", slop)
		}
	}
	// Intentional keeps: favicon handshake + gamification medals.
	for _, keep := range []string{"🤝", "🥇"} {
		if !strings.Contains(body, keep) {
			t.Errorf("UI page lost intentional emoji %q", keep)
		}
	}
}
