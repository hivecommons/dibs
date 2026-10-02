package api

import (
	"net/http"
	"testing"

	"github.com/hivecommons/dibs/pkg/store"
)

// TestMarketSurfacesDeriveSymbolForLegacyIdeas pins the displaySymbol
// fallback: ideas persisted before symbols were assigned at creation carry
// an empty Symbol on disk, and the public board and ticker must still show
// a derived $TICKER rather than an empty string.
func TestMarketSurfacesDeriveSymbolForLegacyIdeas(t *testing.T) {
	a, mux := newAPIFixture(t)
	mux.HandleFunc("GET /api/board", a.HandleBoard)
	mux.HandleFunc("GET /api/ticker", a.HandleTicker)
	modern := mustCreate(t, a, "bob", "Modern kubernetes widget", store.VisibilityPublic, store.StatusDraft)
	legacy := mustCreate(t, a, "bob", "Legacy scheduler plugin", store.VisibilityPublic, store.StatusDraft)
	if _, err := a.Store.Mutate(legacy.ID, false, func(i *store.Idea) error {
		i.Symbol = ""
		return nil
	}); err != nil {
		t.Fatalf("clear legacy symbol: %v", err)
	}
	stored, err := a.Store.Get(legacy.ID)
	if err != nil || stored.Symbol != "" {
		t.Fatalf("legacy idea symbol = %q err=%v, want empty on disk", stored.Symbol, err)
	}
	wantLegacy := store.TickerSymbol(legacy.Title)
	if wantLegacy == "" {
		t.Fatalf("TickerSymbol(%q) = empty", legacy.Title)
	}

	rec := do(t, mux, nil, http.MethodGet, "/api/board", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("board: status=%d body=%s", rec.Code, rec.Body.String())
	}
	board := decodeBody[struct {
		Board []BoardRow `json:"board"`
	}](t, rec).Board
	symbols := map[string]string{}
	for _, row := range board {
		symbols[row.ID] = row.Symbol
	}
	if symbols[legacy.ID] != wantLegacy {
		t.Fatalf("board legacy symbol = %q, want %q (rows=%+v)", symbols[legacy.ID], wantLegacy, board)
	}
	if symbols[modern.ID] != modern.Symbol || modern.Symbol == "" {
		t.Fatalf("board modern symbol = %q, want persisted %q", symbols[modern.ID], modern.Symbol)
	}

	rec = do(t, mux, nil, http.MethodGet, "/api/ticker", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ticker: status=%d body=%s", rec.Code, rec.Body.String())
	}
	ticker := decodeBody[struct {
		Ticker []TickerEntry `json:"ticker"`
	}](t, rec).Ticker
	var sawLegacy, sawModern bool
	for _, e := range ticker {
		if e.Symbol == "" {
			t.Fatalf("ticker entry with empty symbol: %+v", e)
		}
		switch e.Title {
		case legacy.Title:
			sawLegacy = e.Symbol == wantLegacy
		case modern.Title:
			sawModern = e.Symbol == modern.Symbol
		}
	}
	if !sawLegacy || !sawModern {
		t.Fatalf("ticker symbols: legacy=%v modern=%v entries=%+v", sawLegacy, sawModern, ticker)
	}
}
