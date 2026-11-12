package wisprflow

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/ingest"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestMainUsageErrors(t *testing.T) {
	db := t.TempDir()
	withToken := envMap(map[string]string{ingest.EnvToken: "hub-token"})
	cases := []struct {
		name   string
		args   []string
		getenv func(string) string
		goos   string
		code   int
		want   string
	}{
		{"help", []string{"-h"}, withToken, "linux", 0, "usage: dibs ingest wisprflow"},
		{"bad flag", []string{"--bogus"}, withToken, "linux", 2, "bogus"},
		{"extra arg", []string{"--db", db, "now"}, withToken, "linux", 2, "unexpected argument"},
		{"no default db on linux", nil, withToken, "linux", 2, "--db is required on linux"},
		{"no selector", []string{"--db", db, "--trigger", "", "--apps", " "}, withToken, "linux", 2, "--trigger or --apps"},
		{"no token", []string{"--db", db}, envMap(nil), "linux", 2, ingest.EnvToken},
		{"bad server", []string{"--db", db, "--server", "ftp://x"}, withToken, "linux", 2, "--server"},
	}
	for _, tc := range cases {
		var stderr bytes.Buffer
		if code := run(context.Background(), tc.args, tc.getenv, &stderr, tc.goos); code != tc.code || !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("%s: code=%d stderr=%q; want %d containing %q", tc.name, code, stderr.String(), tc.code, tc.want)
		}
	}
}

func TestMainDefaultDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stderr bytes.Buffer
	env := envMap(map[string]string{ingest.EnvToken: "hub-token"})
	if code := run(context.Background(), []string{"--cursor", filepath.Join(t.TempDir(), "c.json")}, env, &stderr, "darwin"); code != 1 || !strings.Contains(stderr.String(), filepath.Join(home, "Library", "Application Support", "Wispr Flow", "flow.sqlite")) {
		t.Fatalf("darwin default: code=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	env = envMap(map[string]string{ingest.EnvToken: "hub-token", EnvDB: filepath.Join(home, "elsewhere.sqlite")})
	if code := Main(context.Background(), []string{"--cursor", filepath.Join(t.TempDir(), "c.json")}, env, &stderr); code != 1 || !strings.Contains(stderr.String(), "elsewhere.sqlite") {
		t.Fatalf("$%s: code=%d stderr=%s", EnvDB, code, stderr.String())
	}
}

func TestMainNeedsHomeForDefaultCursor(t *testing.T) {
	t.Setenv("HOME", "")
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"--db", t.TempDir()}, envMap(map[string]string{ingest.EnvToken: "hub-token"}), &stderr, "linux"); code != 1 {
		t.Fatalf("code = %d, stderr=%s", code, stderr.String())
	}
}

func TestMainIngestsEndToEnd(t *testing.T) {
	srv, st := dibsServer(t)
	dir := t.TempDir()
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	writeFiles(t, dir, map[string]string{
		"history.json": `[
{"id":"w1","text":"Dibs idea: let people claim ideas by voice from anywhere they are","at":"` + at(3*time.Hour) + `","app":"com.apple.Notes"},
{"id":"w2","text":"Running five minutes late, start without me please","at":"` + at(2*time.Hour) + `","app":"com.tinyspeck.slackmacgap"},
{"id":"w3","text":"Idea for dibs, a weekly digest of ideas nobody has claimed yet","at":"` + at(time.Hour) + `","app":"com.tinyspeck.slackmacgap"}
]`,
	})
	cursor := filepath.Join(t.TempDir(), "state", "wisprflow-cursor.json")
	env := envMap(map[string]string{ingest.EnvToken: "hub-token", ingest.EnvServer: srv.URL + "/"})
	args := []string{"--db", dir, "--cursor", cursor}

	var stderr bytes.Buffer
	if code := run(context.Background(), append(args, "--dry-run"), env, &stderr, "linux"); code != 0 || !strings.Contains(stderr.String(), "planned=2") {
		t.Fatalf("dry run: code=%d stderr=%s", code, stderr.String())
	}
	if ideas, _ := st.ListByAuthor("alice"); len(ideas) != 0 {
		t.Fatalf("dry run submitted %d ideas", len(ideas))
	}

	stderr.Reset()
	if code := run(context.Background(), args, env, &stderr, "linux"); code != 0 {
		t.Fatalf("code = %d, stderr=%s", code, stderr.String())
	}
	ideas, err := st.ListByAuthor("alice")
	if err != nil || len(ideas) != 2 {
		t.Fatalf("ideas = %d, %v; stderr=%s", len(ideas), err, stderr.String())
	}
	titles := map[string]string{}
	for _, idea := range ideas {
		titles[idea.Title] = idea.Body
		if strings.Join(idea.Tags, ",") != DefaultTags {
			t.Errorf("tags = %v", idea.Tags)
		}
	}
	body, ok := titles["Let people claim ideas by voice from anywhere they are"]
	if !ok || !strings.Contains(body, "let people claim ideas by voice") || !strings.Contains(body, "in Notes; Wispr Flow transcript w1.") {
		t.Fatalf("ideas = %v", titles)
	}
	if _, ok := titles["A weekly digest of ideas nobody has claimed yet"]; !ok {
		t.Fatalf("ideas = %v", titles)
	}
	if strings.Contains(stderr.String(), "hub-token") {
		t.Fatalf("stderr leaks the token: %s", stderr.String())
	}

	cur, err := ingest.LoadCursor(cursor)
	if err != nil || len(cur.Seen) != 2 || cur.Newest.Format(time.RFC3339) != at(time.Hour) {
		t.Fatalf("cursor = %+v, %v", cur, err)
	}
	if code := run(context.Background(), args, env, &stderr, "linux"); code != 0 {
		t.Fatalf("second run code = %d", code)
	}
	if ideas, _ := st.ListByAuthor("alice"); len(ideas) != 2 {
		t.Fatalf("second run resubmitted: %d ideas", len(ideas))
	}
}
