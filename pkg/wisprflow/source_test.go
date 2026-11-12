package wisprflow

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/deps"
	"github.com/hivecommons/dibs/pkg/ingest"
	"github.com/hivecommons/dibs/pkg/mcpserver"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// The test binary doubles as a fake sqlite3 CLI (helper-process pattern), so
// the exec path is covered on machines without sqlite3: it records its
// arguments to $fakeArgsEnv and prints $fakeOutEnv, or fails like sqlite3
// does when that is "fail".
const (
	fakeEnv     = "DIBS_WISPRFLOW_FAKE_SQLITE3"
	fakeOutEnv  = "DIBS_WISPRFLOW_FAKE_OUT"
	fakeArgsEnv = "DIBS_WISPRFLOW_FAKE_ARGS"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		_ = os.WriteFile(os.Getenv(fakeArgsEnv), []byte(strings.Join(os.Args[1:], "\n")), 0o600)
		if out := os.Getenv(fakeOutEnv); out == "fail" {
			os.Stderr.WriteString("Error: in prepare, no such table: History\nmore\n")
			os.Exit(1)
		} else {
			os.Stdout.WriteString(out)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeSQLite3 makes the test binary answer sqlite3 calls with out and
// returns the file its arguments are recorded in.
func fakeSQLite3(t *testing.T, out string) string {
	t.Helper()
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv(fakeEnv, "1")
	t.Setenv(fakeOutEnv, out)
	t.Setenv(fakeArgsEnv, args)
	return args
}

// dbFile is an existing file standing in for flow.sqlite.
func dbFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "flow.sqlite")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const historyJSON = `[
{"id":"t1","text":"Dibs idea: a voice inbox that turns dictations into drafts","at":"2026-11-11 09:00:00.123 +00:00","app":"com.apple.Notes"},
{"id":"t2","text":"Sounds good, see you at standup tomorrow morning then","at":"2026-11-11 10:00:00.000 +00:00","app":"com.tinyspeck.slackmacgap"},
{"id":"t3","text":"Idea for Dibs — rank ideas by how many people called dibs on them","at":"2026-11-11 08:00:00 +00:00","app":"com.tinyspeck.slackmacgap"},
{"id":"t4","text":"dibs idea too short","at":"2026-11-11 11:00:00 +00:00","app":"com.apple.Notes"}
]`

func newSource(path string) *Source {
	return &Source{Path: path, SQLite3: os.Args[0], Triggers: DefaultTriggers, MinWords: DefaultMinWords, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func ids(items []ingest.Item) string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID())
	}
	return strings.Join(out, ",")
}

func TestListReadsSQLiteAndSelectsIdeas(t *testing.T) {
	argsFile := fakeSQLite3(t, historyJSON)
	db := dbFile(t)
	src := newSource(db)
	since := time.Date(2026, 11, 10, 22, 0, 0, 0, time.UTC)
	items, err := src.List(context.Background(), since)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := ids(items); got != "t1,t3" {
		t.Fatalf("selected %q, want the two triggered dictations with enough words", got)
	}
	if want := time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC); !src.Watermark().Equal(want) {
		t.Fatalf("watermark = %v, want the newest row read (%v), idea or not", src.Watermark(), want)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(string(args), "\n")
	if len(argv) != 5 || argv[0] != "-readonly" || argv[1] != "-json" || argv[3] != db || !strings.Contains(argv[4], "datetime('2026-11-10 22:00:00')") {
		t.Fatalf("sqlite3 args = %q", argv)
	}

	src.Apps = []string{"notes"}
	items, err = src.List(context.Background(), since)
	if err != nil || ids(items) != "t1" {
		t.Fatalf("apps=notes: %q, %v", ids(items), err)
	}
}

func TestListSQLiteFailures(t *testing.T) {
	fakeSQLite3(t, "fail")
	src := newSource(dbFile(t))
	_, err := src.List(context.Background(), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "no such table: History") || strings.Contains(err.Error(), "more") {
		t.Fatalf("err = %v, want sqlite3's first stderr line", err)
	}

	src.SQLite3 = filepath.Join(t.TempDir(), "no-sqlite3")
	if _, err := src.List(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), "sqlite3 failed") {
		t.Fatalf("missing binary: err = %v", err)
	}

	fakeSQLite3(t, "not json")
	src.SQLite3 = os.Args[0]
	if _, err := src.List(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("bad output: err = %v", err)
	}

	src.Path = filepath.Join(t.TempDir(), "missing.sqlite")
	if _, err := src.List(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), "--db") {
		t.Fatalf("missing db: err = %v", err)
	}
}

func TestListDefaultsSQLite3Command(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path)
	src := newSource(dbFile(t))
	src.SQLite3 = " "
	if _, err := src.List(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), DefaultSQLite3) {
		t.Fatalf("err = %v, want the default sqlite3 looked up on PATH", err)
	}
}

// TestListRealSQLite runs the query against a real History table, with a
// BLOB column that must never be selected.
func TestListRealSQLite(t *testing.T) {
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 CLI not installed")
	}
	db := filepath.Join(t.TempDir(), "flow.sqlite")
	schema := `CREATE TABLE History (transcriptEntityId TEXT PRIMARY KEY, asrText TEXT, formattedText TEXT, editedText TEXT, timestamp DATETIME, app TEXT, url TEXT, duration REAL, numWords INTEGER, status TEXT, isArchived TINYINT, audio BLOB);
INSERT INTO History VALUES ('a','dibs idea raw','Dibs idea: build a voice inbox for every team',NULL,'2026-11-11 09:00:00.123 +00:00','com.apple.Notes','',3.5,9,'formatted',0,x'00ff');
INSERT INTO History VALUES ('b','dibs idea raw','Dibs idea: build a voice inbox','Dibs idea: let ideas be claimed by voice from anywhere','2026-11-11 10:00:00.000 +00:00','com.apple.Notes','',2,9,'formatted',0,x'00');
INSERT INTO History VALUES ('c','dibs idea archived thing that should never show','',NULL,'2026-11-11 10:30:00.000 +00:00','com.apple.Notes','',2,9,'formatted',1,NULL);
INSERT INTO History VALUES ('d','dibs idea still being formatted right now ok','',NULL,'2026-11-11 10:40:00.000 +00:00','com.apple.Notes','',2,9,'raw_transcript',0,NULL);
INSERT INTO History VALUES ('e','Dibs idea: too old to matter at all here','',NULL,'2026-11-01 10:00:00.000 +00:00','com.apple.Notes','',2,9,'formatted',0,NULL);`
	if out, err := exec.Command(bin, db, schema).CombinedOutput(); err != nil {
		t.Fatalf("creating db: %v: %s", err, out)
	}
	src := newSource(db)
	src.SQLite3 = bin
	items, err := src.List(context.Background(), time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if ids(items) != "a,b" {
		t.Fatalf("selected %q, want the finalised, unarchived rows in the window", ids(items))
	}
	idea, _ := items[1].Idea(context.Background())
	if idea.Title != "Let ideas be claimed by voice from anywhere" {
		t.Fatalf("editedText not preferred: title %q", idea.Title)
	}
	if want := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC); !src.Watermark().Equal(want) {
		t.Fatalf("watermark = %v, want %v", src.Watermark(), want)
	}
}

func TestHistorySQLSelectsNoBlobs(t *testing.T) {
	q := historySQL(time.Date(2026, 11, 11, 9, 0, 0, 0, time.FixedZone("CET", 3600)), 10)
	for _, want := range []string{"FROM History", "status='formatted'", "COALESCE(isArchived,0)=0", "datetime('2026-11-11 08:00:00')", "LIMIT 10", "NULLIF(editedText,'')"} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q: %s", want, q)
		}
	}
	if strings.Contains(q, "*") || strings.Contains(strings.ToLower(q), "audio") {
		t.Errorf("query must name only text columns: %s", q)
	}
}

func TestSelectIdea(t *testing.T) {
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		text     string
		app      string
		triggers []string
		apps     []string
		want     string
		ok       bool
	}{
		{"colon", "Dibs idea: build a voice inbox now", "", DefaultTriggers, nil, "build a voice inbox now", true},
		{"comma and case", "  dibs, IDEA — build a voice inbox now", "", DefaultTriggers, nil, "build a voice inbox now", true},
		{"second trigger", "Idea for Dibs. Build a voice inbox now", "", DefaultTriggers, nil, "Build a voice inbox now", true},
		{"no trigger", "Build a voice inbox now please", "", DefaultTriggers, nil, "", false},
		{"trigger mid-text", "I said dibs idea: build a voice inbox", "", DefaultTriggers, nil, "", false},
		{"trigger word prefix", "Dibs ideas are great when they come in", "", DefaultTriggers, nil, "", false},
		{"too short after trigger", "Dibs idea: voice inbox", "", DefaultTriggers, nil, "", false},
		{"trigger only", "Dibs idea.", "", DefaultTriggers, nil, "", false},
		{"apps only", "Build a voice inbox now please", "com.apple.Notes", nil, []string{"com.apple.notes"}, "Build a voice inbox now please", true},
		{"apps by name", "Build a voice inbox now please", "com.apple.Notes", nil, []string{" Notes "}, "Build a voice inbox now please", true},
		{"app not allowed", "Dibs idea: build a voice inbox now", "com.tinyspeck.slackmacgap", DefaultTriggers, []string{"notes", ""}, "", false},
		{"blank trigger ignored", "Build a voice inbox now please", "md.obsidian", []string{" , "}, []string{"obsidian"}, "Build a voice inbox now please", true},
	}
	for _, tc := range cases {
		s := &Source{Triggers: tc.triggers, Apps: tc.apps, MinWords: DefaultMinWords}
		got, ok := s.selectIdea(Dictation{ID: "x", Text: tc.text, At: at, App: tc.app}, triggerMatchers(s.Triggers))
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	if _, ok := (&Source{}).selectIdea(Dictation{Text: "no id at all here today ok"}, nil); ok {
		t.Error("a dictation without id was selected")
	}
}

func TestIdeaRendering(t *testing.T) {
	d := Dictation{ID: "t-9", At: time.Date(2026, 11, 11, 9, 5, 0, 0, time.UTC), App: "com.tinyspeck.slackmacgap"}
	it := &item{d: d, body: "build a voice inbox that turns every dictation into a draft, then rank them."}
	idea, err := it.Idea(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if idea.Title != "Build a voice inbox that turns every dictation into a…" {
		t.Fatalf("title = %q", idea.Title)
	}
	if !strings.HasPrefix(idea.Description, it.body+"\n\n---\n") || !strings.Contains(idea.Description, "Dictated with Wispr Flow on 2026-11-11T09:05:00Z in Slack; Wispr Flow transcript t-9.") {
		t.Fatalf("description = %q", idea.Description)
	}
	if !it.When().Equal(d.At) {
		t.Fatalf("When = %v", it.When())
	}

	if got := ideaTitle("draft, then rank them, all eleven words, here, now, ok."); got != "Draft, then rank them, all eleven words, here, now, ok." {
		t.Fatalf("short title = %q", got)
	}
	if got := ideaTitle("draft, then rank them, all of these eleven words, here, now"); got != "Draft, then rank them, all of these eleven words, here…" {
		t.Fatalf("punctuation before ellipsis: %q", got)
	}
	long := ideaTitle(strings.Repeat("é", store.MaxTitleLen+5))
	if n := len([]rune(long)); n != store.MaxTitleLen {
		t.Fatalf("long title has %d runes", n)
	}

	huge := strings.Repeat("ü", store.MaxBodyBytes)
	desc := ideaDescription(Dictation{ID: "big", At: d.At}, huge)
	if len(desc) > store.MaxBodyBytes || !strings.Contains(desc, truncatedMarker) || !strings.HasSuffix(desc, "transcript big._\n") || strings.Contains(desc, " in ") {
		t.Fatalf("truncated description: %d bytes, tail %q", len(desc), desc[len(desc)-120:])
	}
}

func TestAppName(t *testing.T) {
	for in, want := range map[string]string{
		"com.tinyspeck.slackmacgap": "Slack",
		"com.apple.Notes":           "Notes",
		"com.example.Writer":        "Writer",
		"Notes":                     "Notes",
		"weird.":                    "weird.",
		"":                          "",
	} {
		if got := appName(in); got != want {
			t.Errorf("appName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	home := func() (string, error) { return "/Users/me", nil }
	noHome := func() (string, error) { return "", os.ErrNotExist }
	env := func(k string) string {
		if k == "APPDATA" {
			return `C:\Users\me\AppData\Roaming`
		}
		return ""
	}
	if got := DefaultPath("darwin", home, env); got != filepath.Join("/Users/me", "Library", "Application Support", "Wispr Flow", "flow.sqlite") {
		t.Errorf("darwin = %q", got)
	}
	if got := DefaultPath("windows", home, env); got != filepath.Join(`C:\Users\me\AppData\Roaming`, "Wispr Flow", "flow.sqlite") {
		t.Errorf("windows = %q", got)
	}
	for _, got := range []string{DefaultPath("linux", home, env), DefaultPath("darwin", noHome, env), DefaultPath("windows", home, func(string) string { return "" })} {
		if got != "" {
			t.Errorf("want no default, got %q", got)
		}
	}
}

// dibsServer runs the real Dibs MCP handler at /mcp, authenticating the
// bearer token "hub-token" as alice.
func dibsServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewHandler(mcpserver.Config{
		Hub:  &auth.FakeHub{BearerTokens: map[string]auth.Identity{"hub-token": {Username: "alice", DisplayName: "Alice"}}},
		Deps: deps.Deps{Store: st, Registry: reg},
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}
