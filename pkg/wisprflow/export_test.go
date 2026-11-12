package wisprflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadExportDir(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"history.json":                  `{"dictations":[{"transcriptEntityId":"j1","editedText":"Dibs idea: json export works fine today","timestamp":"2026-11-11T09:00:00Z","app":"com.apple.Notes"},{"id":"old","text":"too old","timestamp":"2026-10-01T00:00:00Z"}]}`,
		"more.ndjson":                   "{\"id\":\"n1\",\"formatted_text\":\"ndjson line\",\"created_at\":1794474000000}\n\n{\"id\":\"j1\",\"text\":\"duplicate id\",\"at\":\"2026-11-11T09:30:00Z\"}\n",
		"sheet.csv":                     "\ufeffid,text,timestamp,app\nc1,csv row,2026-11-11 10:00:00,md.obsidian\nc2,,2026-11-11 10:00:00,x\n",
		"empty.csv":                     "",
		"notes/2026-11-11-1130-idea.md": "---\nid: m1\napp: \"com.apple.Notes\"\nnot front matter\n---\n# Heading\n\nmarkdown body\n",
		"notes/plain.txt":               "---\ntimestamp: 2026-11-11T12:00\n---\ntxt body\n",
		"notes/named-2026-11-11T13-05-09.markdown": "named body",
		"notes/stamp.md":   "---\ndate: 2026-11-11_14h05\n---\nstamp body",
		"notes/heading.md": "# Only a heading",
		"notes/mtime.md":   "mtime body",
		"unknown.pdf":      "ignored",
		".hidden.json":     `[{"id":"h1","text":"hidden","at":"2026-11-11T09:00:00Z"}]`,
		".git/x.json":      `[{"id":"g1","text":"git","at":"2026-11-11T09:00:00Z"}]`,
	})
	mtime := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "notes", "mtime.md"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	rows, err := readExportDir(dir, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("readExportDir: %v", err)
	}
	got := map[string]Dictation{}
	for _, r := range rows {
		got[r.ID] = r
	}
	want := map[string]string{
		"j1":                              "Dibs idea: json export works fine today",
		"n1":                              "ndjson line",
		"c1":                              "csv row",
		"m1":                              "markdown body",
		"notes/plain":                     "txt body",
		"notes/named-2026-11-11T13-05-09": "named body",
		"notes/stamp":                     "stamp body",
		"notes/mtime":                     "mtime body",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for id, text := range want {
		if got[id].Text != text {
			t.Errorf("%s: text %q, want %q", id, got[id].Text, text)
		}
	}
	times := map[string]time.Time{
		"n1":                              time.UnixMilli(1794474000000).UTC(),
		"m1":                              time.Date(2026, 11, 11, 11, 30, 0, 0, time.UTC),
		"notes/plain":                     time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC),
		"notes/named-2026-11-11T13-05-09": time.Date(2026, 11, 11, 13, 5, 9, 0, time.UTC),
		"notes/stamp":                     time.Date(2026, 11, 11, 14, 5, 0, 0, time.UTC),
		"notes/mtime":                     mtime,
	}
	for id, at := range times {
		if !got[id].At.Equal(at) {
			t.Errorf("%s: at %v, want %v", id, got[id].At, at)
		}
	}
	if got["m1"].App != "com.apple.Notes" || got["c1"].App != "md.obsidian" {
		t.Errorf("apps: m1=%q c1=%q", got["m1"].App, got["c1"].App)
	}
}

func TestReadExportDirReportsBadFiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"good.json": `[{"id":"g","text":"good","at":"2026-11-11T09:00:00Z"}]`,
		"bad.json":  `{nope`,
		"bad.jsonl": "{\"id\":\"b1\",\"text\":\"kept\",\"at\":\"2026-11-11T09:00:00Z\"}\nnot json\n",
	})
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "dangling.json")); err != nil {
		t.Fatal(err)
	}
	rows, err := readExportDir(dir, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "3 export file(s) skipped") || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want the readable ones", rows)
	}
}

func TestListExportFolder(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.json": `[{"id":"x2","text":"Dibs idea: second idea from the export folder","at":"2026-11-11T10:00:00Z"},{"id":"x1","text":"Dibs idea: first idea from the export folder","at":"2026-11-11T09:00:00Z"},{"id":"x3","text":"just a chat message for slack","at":"2026-11-11T11:00:00Z"}]`,
	})
	src := newSource(dir)
	items, err := src.List(context.Background(), time.Time{})
	if err != nil || ids(items) != "x1,x2" {
		t.Fatalf("items %q, %v", ids(items), err)
	}
	if want := time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC); !src.Watermark().Equal(want) {
		t.Fatalf("watermark = %v, want %v", src.Watermark(), want)
	}

	writeFiles(t, dir, map[string]string{"broken.json": "{"})
	items, err = src.List(context.Background(), time.Time{})
	if err != nil || len(items) != 2 || !src.Watermark().IsZero() {
		t.Fatalf("partly unreadable folder: %d items, %v, watermark %v; want items kept and no watermark", len(items), err, src.Watermark())
	}
}

func TestListExportFolderCapsRows(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := maxRowsPerRun + 5; i > 0; i-- {
		at := start.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		b.WriteString(`{"id":"r` + at + `","text":"Dibs idea: one more export row here","at":"` + at + `"}` + "\n")
	}
	writeFiles(t, dir, map[string]string{"rows.ndjson": b.String()})
	src := newSource(dir)
	items, err := src.List(context.Background(), time.Time{})
	if err != nil || len(items) != maxRowsPerRun {
		t.Fatalf("items = %d, %v", len(items), err)
	}
	if want := start.Add(maxRowsPerRun * time.Minute); !src.Watermark().Equal(want) {
		t.Fatalf("watermark = %v, want the oldest %d rows read (%v)", src.Watermark(), maxRowsPerRun, want)
	}
}

func TestDecodeMarkdownWithoutFrontMatterEnd(t *testing.T) {
	d, ok := decodeMarkdown("x.md", []byte("---\nid: never closed\nbody"), time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	if !ok || d.ID != "x" || !strings.Contains(d.Text, "never closed") {
		t.Fatalf("d = %+v, %v", d, ok)
	}
	d, _ = decodeMarkdown("y.md", []byte("---\ntranscriptEntityId: t-7\ndate: someday\n---\nbody"), time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	if d.ID != "t-7" || !d.At.Equal(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("d = %+v; want front matter id and mtime for an unparseable date", d)
	}
}

func TestTimeFromName(t *testing.T) {
	if _, ok := timeFromName("notes.md"); ok {
		t.Fatal("no date: want false")
	}
	if _, ok := timeFromName("2026-13-45.md"); ok {
		t.Fatal("invalid date: want false")
	}
	if got, ok := timeFromName("2026-11-11.md"); !ok || !got.Equal(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("date only = %v, %v", got, ok)
	}
}

func TestReadExportFileRejectsOversizeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.json")
	// A sparse file reports the size without writing 64 MiB to disk.
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, maxExportFileBytes+1); err != nil {
		t.Fatal(err)
	}
	rows, err := readExportFile(dir, p)
	if err == nil || !strings.Contains(err.Error(), "file larger than") {
		t.Fatalf("err = %v, want the size cap", err)
	}
	if rows != nil {
		t.Fatalf("rows = %+v, want none", rows)
	}
	_, err = readExportDir(dir, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "1 export file(s) skipped: huge.json: file larger than") {
		t.Fatalf("readExportDir err = %v, want the oversize file reported", err)
	}
}

func TestReadExportDirReportsUnreadableSubdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"good.json":         `[{"id":"g","text":"good","at":"2026-11-11T09:00:00Z"}]`,
		"locked/inner.json": `[{"id":"l","text":"locked","at":"2026-11-11T09:00:00Z"}]`,
	})
	locked := filepath.Join(dir, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	rows, err := readExportDir(dir, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "1 export file(s) skipped: "+locked+":") {
		t.Fatalf("err = %v, want the unreadable directory reported", err)
	}
	if len(rows) != 1 || rows[0].ID != "g" {
		t.Fatalf("rows = %+v, want only the readable file", rows)
	}
}
