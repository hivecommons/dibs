// Ticker symbols: every idea gets a short $UPPERCASE market-style symbol
// derived from its title ("Dark mode for dashboards" → $DARKMODE). Symbols
// are display sugar for the market-landing UI — the idea ID stays the only
// real identifier — but they are persisted and uniquified at creation time
// so the tape and the board never show two ideas under one symbol.
package store

import (
	"fmt"
	"strings"
	"unicode"
)

// MaxSymbolLen caps a ticker symbol's length (excluding the "$" the UI adds).
const MaxSymbolLen = 12

// symbolStopwords are words too generic to spend symbol characters on.
var symbolStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "on": true, "for": true, "with": true, "by": true,
	"at": true, "is": true, "are": true, "be": true, "as": true, "it": true,
	"that": true, "this": true, "from": true, "into": true, "via": true,
	"my": true, "our": true, "your": true, "new": true, "add": true,
	"support": true, "make": true, "allow": true, "enable": true,
}

// TickerSymbol derives a base symbol from a title: the first significant
// words, uppercased, ASCII letters and digits only, at most MaxSymbolLen
// chars. It never returns "" — a title with no usable characters yields
// "IDEA".
func TickerSymbol(title string) string {
	var words []string
	for _, w := range strings.FieldsFunc(title, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if symbolStopwords[strings.ToLower(w)] {
			continue
		}
		// Symbols are ASCII: drop any rune outside A-Z / 0-9 after
		// uppercasing (accented letters, emoji fragments, etc.).
		var clean strings.Builder
		for _, r := range strings.ToUpper(w) {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				clean.WriteRune(r)
			}
		}
		if clean.Len() > 0 {
			words = append(words, clean.String())
		}
	}
	var sym strings.Builder
	for _, w := range words {
		if sym.Len() >= MaxSymbolLen {
			break
		}
		room := MaxSymbolLen - sym.Len()
		if len(w) > room {
			if sym.Len() == 0 {
				sym.WriteString(w[:room])
			}
			break
		}
		sym.WriteString(w)
	}
	if sym.Len() == 0 {
		return "IDEA"
	}
	return sym.String()
}

// uniqueSymbol returns base, or base with a numeric suffix (trimmed to fit
// MaxSymbolLen) if base is already taken.
func uniqueSymbol(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf("%d", n)
		stem := base
		if len(stem)+len(suffix) > MaxSymbolLen {
			stem = stem[:MaxSymbolLen-len(suffix)]
		}
		if candidate := stem + suffix; !taken(candidate) {
			return candidate
		}
	}
}

// symbolTakenLocked reports whether any indexed idea already carries sym.
// Caller holds s.mu.
func (s *Store) symbolTakenLocked(sym string) bool {
	for _, e := range s.index {
		if e.Symbol == sym {
			return true
		}
	}
	return false
}
