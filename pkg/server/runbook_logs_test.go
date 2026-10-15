package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The runbooks tell an operator which log lines to grep for and which jq
// selectors to apply to the JSON log stream. Nothing ties those strings to
// the slog messages the binary actually emits: a reworded message, or a
// runbook that quotes the message from memory, leaves the operator grepping
// for a line that never appears. These tests pin the two sides together the
// same way alertrules_test.go pins the PrometheusRule to /metrics.

const runbooksDir = "../../runbooks"

// emittedLog is one slog.Info/Warn/Error call site in non-test source.
type emittedLog struct {
	keys map[string]bool // attribute keys passed as string literals
	at   string          // file:line of the first call site
}

// loadEmittedLogs walks the module for every direct slog.{Info,Warn,Error,Debug}
// call whose message is a string literal and returns message -> attribute keys.
// All logging in this module goes through those four package-level helpers
// (no slog.With/Logger values), so the scan is complete.
func loadEmittedLogs(t *testing.T) map[string]*emittedLog {
	t.Helper()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	out := map[string]*emittedLog{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "slog" {
				return true
			}
			switch sel.Sel.Name {
			case "Info", "Warn", "Error", "Debug":
			default:
				return true
			}
			msg, ok := stringLit(call.Args[0])
			if !ok {
				return true
			}
			e := out[msg]
			if e == nil {
				e = &emittedLog{keys: map[string]bool{}, at: fset.Position(call.Pos()).String()}
				out[msg] = e
			}
			// Attributes are alternating key, value after the message.
			for i := 1; i < len(call.Args); i += 2 {
				if k, ok := stringLit(call.Args[i]); ok {
					e.keys[k] = true
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scanning module for slog calls: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("found no slog call sites; scan is broken")
	}
	return out
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

type runbookRef struct {
	file, text string
}

// runbookRefs returns every backtick span and every double-quoted string in
// runbooks/*.md, each tagged with its file.
func runbookRefs(t *testing.T) []runbookRef {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(runbooksDir, "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob %s: %v (%d files)", runbooksDir, err, len(files))
	}
	backtick := regexp.MustCompile("`([^`\n]+)`")
	quoted := regexp.MustCompile(`"([^"\n]+)"`)
	var refs []runbookRef
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, re := range []*regexp.Regexp{backtick, quoted} {
			for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
				refs = append(refs, runbookRef{file: filepath.Base(f), text: m[1]})
			}
		}
	}
	return refs
}

// logMessageRef normalizes a runbook snippet to the log message it names:
// trailing "..." / ": ..." elision is dropped and intra-word punctuation is
// stripped, so `readyz: store ping failed: ...` compares against the emitted
// message by its words rather than its punctuation.
func normalizeWords(s string) []string {
	s = strings.TrimSpace(s)
	for _, suffix := range []string{": ...", " ...", "..."} {
		s = strings.TrimSuffix(s, suffix)
	}
	s = strings.ReplaceAll(s, ":", " ")
	return strings.Fields(strings.ToLower(s))
}

// TestRunbookLogReferencesMatchEmittedMessages checks that every runbook
// snippet that names a log message (shares its first two words with an
// emitted slog message) is the exact emitted text. Operators copy these
// strings into grep/jq against the JSON log stream, where any punctuation
// or wording difference is a silent miss.
func TestRunbookLogReferencesMatchEmittedMessages(t *testing.T) {
	emitted := loadEmittedLogs(t)
	byPrefix := map[string][]string{}
	for msg := range emitted {
		w := normalizeWords(msg)
		if len(w) < 2 {
			continue
		}
		k := w[0] + " " + w[1]
		byPrefix[k] = append(byPrefix[k], msg)
	}

	matched := 0
	for _, ref := range runbookRefs(t) {
		w := normalizeWords(ref.text)
		if len(w) < 2 {
			continue
		}
		candidates := byPrefix[w[0]+" "+w[1]]
		if len(candidates) == 0 {
			continue
		}
		// A reference may legitimately name only the message, or the
		// message followed by its elided detail; it must not reword it.
		text := strings.TrimSpace(ref.text)
		for _, suffix := range []string{": ...", " ...", "..."} {
			text = strings.TrimSuffix(text, suffix)
		}
		text = strings.TrimSuffix(text, ":")
		ok := false
		for _, msg := range candidates {
			if text == msg {
				ok = true
				break
			}
		}
		if !ok {
			sort.Strings(candidates)
			t.Errorf("%s: %q names a log line but no slog call emits it; emitted messages with that prefix: %q (first at %s)",
				ref.file, ref.text, candidates, emitted[candidates[0]].at)
			continue
		}
		matched++
	}
	// The runbooks are expected to cite at least the metrics flush lines
	// and the readiness failure; zero matches means the extraction broke.
	if matched < 4 {
		t.Fatalf("only %d runbook log references matched emitted messages; extraction looks broken", matched)
	}
}

// TestRunbookJQSelectorsUseEmittedKeys checks every
// `select(.msg == "<msg>" and .<key> == ...)` filter in the runbooks: the
// message must be emitted and each selected key must be an attribute of
// that very slog call, otherwise the filter matches nothing.
func TestRunbookJQSelectorsUseEmittedKeys(t *testing.T) {
	emitted := loadEmittedLogs(t)
	msgRe := regexp.MustCompile(`\.msg == "([^"]+)"`)
	keyRe := regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_]*) ==`)

	files, err := filepath.Glob(filepath.Join(runbooksDir, "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob %s: %v", runbooksDir, err)
	}
	selectors := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			m := msgRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			selectors++
			e := emitted[m[1]]
			if e == nil {
				t.Errorf("%s: jq selector %q: no slog call emits msg %q", filepath.Base(f), strings.TrimSpace(line), m[1])
				continue
			}
			for _, km := range keyRe.FindAllStringSubmatch(line, -1) {
				key := km[1]
				if key == "msg" {
					continue
				}
				if !e.keys[key] {
					t.Errorf("%s: jq selector %q selects .%s but the %q log line (at %s) has keys %v",
						filepath.Base(f), strings.TrimSpace(line), key, m[1], e.at, keys(e.keys))
				}
			}
		}
	}
	if selectors == 0 {
		t.Fatal("no jq .msg selectors found in runbooks; extraction looks broken")
	}
}
