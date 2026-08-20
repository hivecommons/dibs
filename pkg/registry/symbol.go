package registry

import (
	"sort"
	"strings"
)

const repoSymbolLen = 4

// RepoTickerSymbol derives the preferred 4-letter, A-Z ticker for repoID.
// It only uses the repo-name half of org/repo. Single words are abbreviated
// as the first three letters plus the next consonant (or the fourth letter),
// so "hive" stays HIVE and "bluefin" becomes BLUF. Multi-word names use
// compact, readable contractions: common two-word names take three
// consonants from the first word plus the second word's initial
// ("destiny-videos" → DSTV), acronym-led names keep the acronym plus the
// last word initial ("mcp-context-forge" → MCPF), and other names start from
// word initials. Short names are padded from their own letters, then X.
func RepoTickerSymbol(repoID string) string {
	words, letters := repoSymbolWords(repoID)
	if len(words) == 0 {
		return "XXXX"
	}

	var sym strings.Builder
	if len(words) == 1 {
		w := words[0]
		if len(w) >= repoSymbolLen {
			sym.WriteString(w[:3])
			if c, ok := nextConsonant(w, 3); ok {
				sym.WriteByte(c)
			} else {
				sym.WriteByte(w[3])
			}
		} else {
			sym.WriteString(w)
		}
	} else if isAcronymWord(words[0]) && len(words[0]) >= 2 && len(words[0]) <= 3 {
		sym.WriteString(words[0])
		sym.WriteByte(words[len(words)-1][0])
		for i := 1; sym.Len() < repoSymbolLen && i < len(words)-1; i++ {
			sym.WriteByte(words[i][0])
		}
	} else if len(words) == 2 && consonantCount(words[0]) >= 3 {
		writeConsonants(&sym, words[0], 3)
		sym.WriteByte(words[1][0])
	} else {
		for _, w := range words {
			if sym.Len() == repoSymbolLen {
				break
			}
			sym.WriteByte(w[0])
		}
		if sym.Len() < repoSymbolLen {
			writeConsonants(&sym, words[0], repoSymbolLen-sym.Len())
		}
	}

	return padRepoSymbol(sym.String(), letters)
}

// UniqueRepoTickerSymbol returns RepoTickerSymbol(repoID), or a deterministic
// fallback when that symbol is already assigned. Fallbacks keep the first
// three base letters and vary the last through letters from the repo name,
// then the remaining alphabet, before exhausting two-letter tails.
func UniqueRepoTickerSymbol(repoID string, taken func(string) bool) string {
	base := RepoTickerSymbol(repoID)
	if !taken(base) {
		return base
	}
	letters := repoSymbolLetters(repoID)
	for _, c := range append(uniqueLetters(letters), alphabet()...) {
		candidate := base[:3] + string(c)
		if !taken(candidate) {
			return candidate
		}
	}
	for a := byte('A'); a <= 'Z'; a++ {
		for b := byte('A'); b <= 'Z'; b++ {
			candidate := base[:2] + string([]byte{a, b})
			if !taken(candidate) {
				return candidate
			}
		}
	}
	for a := byte('A'); a <= 'Z'; a++ {
		for b := byte('A'); b <= 'Z'; b++ {
			for c := byte('A'); c <= 'Z'; c++ {
				for d := byte('A'); d <= 'Z'; d++ {
					candidate := string([]byte{a, b, c, d})
					if !taken(candidate) {
						return candidate
					}
				}
			}
		}
	}
	panic("registry: exhausted 4-letter repo ticker symbols")
}

// ensureRepoSymbolsLocked lazily migrates repo profiles that predate symbols.
// Caller holds r.mu. Existing non-empty symbols are never changed.
func (r *Registry) ensureRepoSymbolsLocked() bool {
	ids := make([]string, 0, len(r.repos))
	for id := range r.repos {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	taken := map[string]bool{}
	changed := false
	for _, id := range ids {
		sym := r.repos[id].Symbol
		switch {
		case sym == "":
		case !validRepoSymbol(sym) || taken[sym]:
			r.repos[id].Symbol = ""
			changed = true
		default:
			taken[sym] = true
		}
	}
	for _, id := range ids {
		rp := r.repos[id]
		if rp.Symbol != "" {
			continue
		}
		rp.Symbol = UniqueRepoTickerSymbol(id, func(s string) bool { return taken[s] })
		taken[rp.Symbol] = true
		changed = true
	}
	return changed
}

func validRepoSymbol(sym string) bool {
	if len(sym) != repoSymbolLen {
		return false
	}
	for i := 0; i < len(sym); i++ {
		if sym[i] < 'A' || sym[i] > 'Z' {
			return false
		}
	}
	return true
}

func repoSymbolWords(repoID string) ([]string, string) {
	name := repoID
	if i := strings.LastIndexByte(repoID, '/'); i >= 0 {
		name = repoID[i+1:]
	}
	var words []string
	var letters strings.Builder
	for _, raw := range strings.FieldsFunc(strings.ToUpper(name), func(r rune) bool {
		return r < 'A' || r > 'Z'
	}) {
		if raw == "" {
			continue
		}
		words = append(words, raw)
		letters.WriteString(raw)
	}
	return words, letters.String()
}

func repoSymbolLetters(repoID string) string {
	_, letters := repoSymbolWords(repoID)
	return letters
}

func padRepoSymbol(sym, letters string) string {
	seen := map[byte]bool{}
	for i := 0; i < len(sym); i++ {
		seen[sym[i]] = true
	}
	var out strings.Builder
	out.WriteString(sym)
	for i := 0; out.Len() < repoSymbolLen && i < len(letters); i++ {
		if !seen[letters[i]] {
			out.WriteByte(letters[i])
			seen[letters[i]] = true
		}
	}
	for out.Len() < repoSymbolLen {
		out.WriteByte('X')
	}
	if out.Len() > repoSymbolLen {
		return out.String()[:repoSymbolLen]
	}
	return out.String()
}

func nextConsonant(s string, start int) (byte, bool) {
	for i := start; i < len(s); i++ {
		if isConsonant(s[i]) {
			return s[i], true
		}
	}
	return 0, false
}

func writeConsonants(sym *strings.Builder, s string, n int) {
	for i := 0; i < len(s) && n > 0; i++ {
		if isConsonant(s[i]) {
			sym.WriteByte(s[i])
			n--
		}
	}
}

func consonantCount(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if isConsonant(s[i]) {
			n++
		}
	}
	return n
}

func isAcronymWord(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isConsonant(s[i]) {
			return false
		}
	}
	return true
}

func isConsonant(c byte) bool {
	return c >= 'A' && c <= 'Z' && c != 'A' && c != 'E' && c != 'I' && c != 'O' && c != 'U'
}

func uniqueLetters(s string) []byte {
	seen := map[byte]bool{}
	var out []byte
	for i := 0; i < len(s); i++ {
		if !seen[s[i]] {
			seen[s[i]] = true
			out = append(out, s[i])
		}
	}
	return out
}

func alphabet() []byte {
	out := make([]byte, 26)
	for i := range out {
		out[i] = byte('A' + i)
	}
	return out
}
