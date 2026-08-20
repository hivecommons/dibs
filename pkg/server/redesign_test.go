package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestTerminalHomeForEveryone pins the terminal-redesign invariants: the
// market terminal (tape, chart, stats, live board) is everyone's home page —
// not gated behind the logged-out landing class — the desk lives in its own
// view, and the jump menu replaces the persistent nav row.
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
		// desk moved to its own view; home carries a slim summary strip
		`id="view-desk"`,
		`class="desk-strip" data-auth`,
		"Open your desk",
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
}

// TestEmojiStripSerious pins the founder's tone directive: decorative emojis
// are gone from nav, headers, buttons, tabs, and log lines. Gamification
// emojis (medals, crown, badge emojis from the API), the handshake favicon,
// and the AI-polish spark spinner are the only intentional survivors.
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
		"🌼", "🔸", "🌧️", "💪", "💾", "🏆", "🆕",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("UI page still contains decorative emoji %q", banned)
		}
	}
	// Intentional keeps: favicon handshake + gamification medals.
	for _, keep := range []string{"🤝", "🥇"} {
		if !strings.Contains(body, keep) {
			t.Errorf("UI page lost intentional emoji %q", keep)
		}
	}
}
