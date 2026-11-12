// Package wisprflow ingests ideas dictated with Wispr Flow into Dibs.
//
// Wispr Flow has no dictation-history API: the desktop app keeps every
// dictation in a local SQLite file (flow.sqlite, table History). This
// package reads that file read-only through the sqlite3 CLI (so no cgo
// SQLite driver is needed), or an export folder, and keeps only dictations
// meant as ideas: those starting with a trigger phrase ("dibs idea ..."),
// and/or made in an allow-listed app, with at least a minimum word count.
package wisprflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hivecommons/dibs/pkg/ingest"
	"github.com/hivecommons/dibs/pkg/store"
)

const (
	// DefaultSQLite3 is the sqlite3 CLI; it ships with macOS.
	DefaultSQLite3 = "sqlite3"
	// DefaultMinWords drops dictations too short to be an idea.
	DefaultMinWords = 5
	// RelistOverlap re-reads this far behind the cursor: Flow updates a row
	// in place (raw_transcript → formatted), so a late finalisation is still
	// picked up.
	RelistOverlap = 2 * time.Hour
	// maxRowsPerRun caps dictations read per run; the rest are read next run.
	maxRowsPerRun = 1000
	// titleWords is how many words of the dictation make the idea title.
	titleWords = 10
	// sqlTimeout bounds one sqlite3 invocation.
	sqlTimeout = time.Minute
	// statusFormatted is the terminal row status in Flow's History table.
	statusFormatted = "formatted"
	truncatedMarker = "\n\n_[dictation truncated to fit the Dibs idea size limit]_\n"
)

// DefaultTriggers are the phrases that mark a dictation as an idea.
var DefaultTriggers = []string{"dibs idea", "idea for dibs"}

// DefaultPath is where the desktop app keeps its history on goos ("" when
// there is no known location, e.g. linux).
func DefaultPath(goos string, home func() (string, error), getenv func(string) string) string {
	switch goos {
	case "darwin":
		if h, err := home(); err == nil && h != "" {
			return filepath.Join(h, "Library", "Application Support", "Wispr Flow", "flow.sqlite")
		}
	case "windows":
		if appdata := getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "Wispr Flow", "flow.sqlite")
		}
	}
	return ""
}

// Source lists Wispr Flow dictations that are ideas. It implements
// ingest.Source and ingest.Watermarker.
type Source struct {
	// Path is flow.sqlite (or a copy) or an export folder.
	Path string
	// SQLite3 is the sqlite3 command (whitespace-separated); default
	// DefaultSQLite3.
	SQLite3 string
	// Triggers are phrases (case-insensitive, punctuation-tolerant) a
	// dictation must start with; they are stripped from the idea. Empty
	// means no trigger is required.
	Triggers []string
	// Apps is an allow-list of app bundle ids or names (case-insensitive
	// substrings, e.g. "com.apple.Notes" or "notes"). Empty allows all.
	Apps []string
	// MinWords drops dictations with fewer words after the trigger.
	MinWords int
	Log      *slog.Logger

	mark time.Time
}

// List implements ingest.Source.
func (s *Source) List(ctx context.Context, since time.Time) ([]ingest.Item, error) {
	s.mark = time.Time{}
	info, err := os.Stat(s.Path)
	if err != nil {
		return nil, fmt.Errorf("wisprflow: %w (is Wispr Flow installed? point --db at flow.sqlite or an export folder; on macOS the process may need Full Disk Access)", err)
	}
	var rows []Dictation
	partial := false
	if info.IsDir() {
		rows, err = readExportDir(s.Path, since)
		if err != nil {
			partial = true
			s.log().Warn("wisprflow export folder partly unreadable", "err", err)
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.Before(rows[j].At) })
		if len(rows) > maxRowsPerRun {
			rows = rows[:maxRowsPerRun]
		}
	} else {
		rows, err = s.readSQLite(ctx, since)
		if err != nil {
			return nil, err
		}
	}
	match := triggerMatchers(s.Triggers)
	var items []ingest.Item
	var newest time.Time
	for _, r := range rows {
		if r.At.After(newest) {
			newest = r.At
		}
		if body, ok := s.selectIdea(r, match); ok {
			items = append(items, &item{d: r, body: body})
		}
	}
	if !partial {
		s.mark = newest
	}
	return items, nil
}

// Watermark implements ingest.Watermarker: the newest dictation the last
// List read, idea or not (zero after a partly unreadable export folder, so
// the unreadable files are retried).
func (s *Source) Watermark() time.Time { return s.mark }

func (s *Source) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// selectIdea applies the app allow-list, the trigger and the minimum word
// count, and returns the idea text (trigger stripped).
func (s *Source) selectIdea(d Dictation, triggers []*regexp.Regexp) (string, bool) {
	if d.ID == "" || !appAllowed(d.App, s.Apps) {
		return "", false
	}
	body := strings.TrimSpace(d.Text)
	if len(triggers) > 0 {
		matched := false
		for _, re := range triggers {
			if loc := re.FindStringIndex(body); loc != nil {
				body, matched = strings.TrimSpace(body[loc[1]:]), true
				break
			}
		}
		if !matched {
			return "", false
		}
	}
	if body == "" || len(strings.Fields(body)) < s.MinWords {
		return "", false
	}
	return body, true
}

// triggerMatchers compiles each trigger phrase to match at the start of a
// dictation regardless of case and of the punctuation Flow's formatting
// adds ("Dibs idea: ...", "dibs, idea — ...").
func triggerMatchers(triggers []string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, t := range triggers {
		words := strings.FieldsFunc(t, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
		if len(words) == 0 {
			continue
		}
		for i, w := range words {
			words[i] = regexp.QuoteMeta(w)
		}
		out = append(out, regexp.MustCompile(`(?i)^[\s\p{P}]*`+strings.Join(words, `[\s\p{P}]+`)+`(?:[\s\p{P}]+|$)`))
	}
	return out
}

func appAllowed(app string, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	app = strings.ToLower(app)
	name := strings.ToLower(appName(app))
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" && (strings.Contains(app, a) || name == a) {
			return true
		}
	}
	return false
}

// knownApps maps common bundle ids to readable names (keys lower-case).
var knownApps = map[string]string{
	"com.tinyspeck.slackmacgap":      "Slack",
	"md.obsidian":                    "Obsidian",
	"com.apple.mobilesms":            "Messages",
	"com.microsoft.vscode":           "VS Code",
	"com.google.chrome":              "Chrome",
	"com.apple.safari":               "Safari",
	"com.electron.wispr-flow":        "Wispr Flow",
	"com.openai.chat":                "ChatGPT",
	"com.anthropic.claudefordesktop": "Claude",
	"com.apple.notes":                "Notes",
	"com.apple.mail":                 "Mail",
	"com.apple.terminal":             "Terminal",
	"com.googlecode.iterm2":          "iTerm2",
	"com.todesktop.230313mzl4w4u92":  "Cursor",
}

// appName turns a bundle id like com.tinyspeck.slackmacgap into a readable
// label; unknown ids keep their last segment.
func appName(app string) string {
	app = strings.TrimSpace(app)
	if n, ok := knownApps[strings.ToLower(app)]; ok {
		return n
	}
	if i := strings.LastIndex(app, "."); i >= 0 && i < len(app)-1 {
		return app[i+1:]
	}
	return app
}

// item is one dictation selected as an idea.
type item struct {
	d    Dictation
	body string
}

func (it *item) ID() string      { return it.d.ID }
func (it *item) When() time.Time { return it.d.At }

// Idea renders the title (the first words after the trigger) and the
// description (the full text plus a footer naming time and app).
func (it *item) Idea(context.Context) (ingest.Idea, error) {
	return ingest.Idea{Title: ideaTitle(it.body), Description: ideaDescription(it.d, it.body)}, nil
}

func ideaTitle(body string) string {
	words := strings.Fields(body)
	title := strings.Join(words, " ")
	if len(words) > titleWords {
		title = strings.TrimRightFunc(strings.Join(words[:titleWords], " "), unicode.IsPunct) + "…"
	}
	if r, size := utf8.DecodeRuneInString(title); size > 0 {
		title = string(unicode.ToUpper(r)) + title[size:]
	}
	if utf8.RuneCountInString(title) > store.MaxTitleLen {
		title = string([]rune(title)[:store.MaxTitleLen-1]) + "…"
	}
	return title
}

func ideaDescription(d Dictation, body string) string {
	footer := "\n\n---\n_Dictated with Wispr Flow on " + d.At.UTC().Format(time.RFC3339)
	if name := appName(d.App); name != "" {
		footer += " in " + name
	}
	footer += "; Wispr Flow transcript " + d.ID + "._\n"
	if limit := store.MaxBodyBytes - len(footer); len(body) > limit {
		cut := limit - len(truncatedMarker)
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + truncatedMarker
	}
	return body + footer
}

// historySQL selects finalised, unarchived dictations newer than since,
// oldest first. Only text columns are selected: History rows also hold
// audio and screenshot BLOBs. Flow stores Sequelize DATETIME strings;
// datetime() normalises them so the comparison does not depend on the
// stored format.
func historySQL(since time.Time, limit int) string {
	return fmt.Sprintf(`SELECT transcriptEntityId AS id, `+
		`COALESCE(NULLIF(editedText,''), NULLIF(formattedText,''), asrText) AS text, `+
		`timestamp AS at, COALESCE(app,'') AS app `+
		`FROM History WHERE COALESCE(isArchived,0)=0 AND status='%s' AND datetime(timestamp) > datetime('%s') `+
		`ORDER BY datetime(timestamp) ASC LIMIT %d;`, statusFormatted, since.UTC().Format("2006-01-02 15:04:05"), limit)
}

// readSQLite runs the sqlite3 CLI read-only with JSON output.
func (s *Source) readSQLite(ctx context.Context, since time.Time) ([]Dictation, error) {
	argv := strings.Fields(s.SQLite3)
	if len(argv) == 0 {
		argv = []string{DefaultSQLite3}
	}
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	args := append(argv[1:len(argv):len(argv)], "-readonly", "-json", "-bail", s.Path, historySQL(since, maxRowsPerRun))
	out, err := exec.CommandContext(ctx, argv[0], args...).Output()
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg = firstLine(string(ee.Stderr))
		}
		return nil, fmt.Errorf("wisprflow: sqlite3 failed: %s", msg)
	}
	return decodeJSON(out)
}
