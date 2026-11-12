package ingest

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultServer is the public Dibs origin.
	DefaultServer = "https://dibs.hivecommons.dev"
	// EnvToken holds the hub bearer token (preferred over --token, which is
	// visible in the process list).
	EnvToken = "DIBS_TOKEN"
	// EnvServer overrides the default --server.
	EnvServer = "DIBS_URL"
)

// Flags are the flags every `dibs ingest <source>` command shares.
type Flags struct {
	source    string
	server    *string
	token     *string
	interval  *time.Duration
	cursor    *string
	sinceDays *int
	tags      *string
	perHour   *int
	perDay    *int
	maxRun    *int
	dryRun    *bool
}

// AddFlags registers the shared flags on fs for `dibs ingest <source>`.
// unit names what the source lists (e.g. "recordings") in help text.
func AddFlags(fs *flag.FlagSet, source, unit, defaultTags string, getenv func(string) string) *Flags {
	return &Flags{
		source:    source,
		server:    fs.String("server", orDefault(getenv(EnvServer), DefaultServer), "Dibs base URL (including any base path); $"+EnvServer),
		token:     fs.String("token", "", "hub bearer token; prefer $"+EnvToken+" since flags are visible to other local users"),
		interval:  fs.Duration("interval", 0, "poll interval (e.g. 15m); 0 runs once, for cron/launchd"),
		cursor:    fs.String("cursor", "", "cursor file (default ~/.dibs/"+source+"-cursor.json)"),
		sinceDays: fs.Int("since-days", DefaultSinceDays, "how many days back the first run looks"),
		tags:      fs.String("tags", defaultTags, "comma-separated tags for ingested ideas"),
		perHour:   fs.Int("max-per-hour", DefaultMaxPerHour, "submissions allowed per rolling hour (0 = unlimited)"),
		perDay:    fs.Int("max-per-day", DefaultMaxPerDay, "submissions allowed per rolling 24h (0 = unlimited)"),
		maxRun:    fs.Int("max", 0, unit+" handled per run (0 = no cap)"),
		dryRun:    fs.Bool("dry-run", false, "log what would be submitted without submitting or advancing the cursor"),
	}
}

// Interval is the parsed --interval.
func (f *Flags) Interval() time.Duration { return *f.interval }

// Configure validates the parsed shared flags and fills in the matching
// fields of in (Name, Sink, CursorPath, Tags, SinceDays, budgets, DryRun,
// Log). On a problem it explains it on stderr and returns the exit code
// (2 usage, 1 environment); 0 means in is ready.
func (f *Flags) Configure(in *Ingester, getenv func(string) string, stderr io.Writer) int {
	prefix := "dibs ingest " + f.source + ": "
	tok := orDefault(*f.token, getenv(EnvToken))
	if tok == "" {
		fmt.Fprintf(stderr, "%sa hub bearer token is required: set $%s\n", prefix, EnvToken)
		return 2
	}
	u, err := url.Parse(*f.server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fmt.Fprintf(stderr, "%s--server must be an http(s) URL, got %q\n", prefix, *f.server)
		return 2
	}
	path := *f.cursor
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "%slocating home directory for the cursor file: %v\n", prefix, err)
			return 1
		}
		path = filepath.Join(home, ".dibs", f.source+"-cursor.json")
	}
	in.Name = f.source
	in.Sink = &DibsSink{Endpoint: strings.TrimRight(*f.server, "/") + "/mcp", Token: tok}
	in.CursorPath = path
	in.Tags = SplitList(*f.tags)
	in.SinceDays = *f.sinceDays
	in.MaxPerHour = *f.perHour
	in.MaxPerDay = *f.perDay
	in.MaxPerRun = *f.maxRun
	in.DryRun = *f.dryRun
	in.Log = slog.New(slog.NewTextHandler(stderr, nil))
	return 0
}

// SplitList splits a comma-separated flag value, dropping blanks.
func SplitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
