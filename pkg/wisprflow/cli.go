package wisprflow

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/hivecommons/dibs/pkg/ingest"
)

const (
	// DefaultTags are attached to every ingested idea.
	DefaultTags = "wisprflow,voice-note"
	// EnvDB overrides the default --db.
	EnvDB = "DIBS_WISPRFLOW_DB"
)

// Main is `dibs ingest wisprflow`: it returns a process exit code (0 ok, 1
// run failure, 2 usage error). getenv is injected for tests.
func Main(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	return run(ctx, args, getenv, stderr, runtime.GOOS)
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer, goos string) int {
	fs := flag.NewFlagSet("dibs ingest wisprflow", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: dibs ingest wisprflow [flags]\n\nReads Wispr Flow's local dictation history and submits each dictation that starts with a\ntrigger phrase (and/or was made in an allowed app) as a private draft Dibs idea.\nThe hub bearer token is read from $%s (or --token).\n\n", ingest.EnvToken)
		fs.PrintDefaults()
	}
	shared := ingest.AddFlags(fs, "wisprflow", "dictations", DefaultTags, getenv)
	defaultDB := getenv(EnvDB)
	if defaultDB == "" {
		defaultDB = DefaultPath(goos, os.UserHomeDir, getenv)
	}
	db := fs.String("db", defaultDB, "Wispr Flow flow.sqlite (or a copy) or an export folder of .json/.ndjson/.csv/.md files; $"+EnvDB)
	trigger := fs.String("trigger", strings.Join(DefaultTriggers, ","), "comma-separated phrases a dictation must start with to be an idea (stripped from it); empty requires none")
	apps := fs.String("apps", "", "comma-separated apps (bundle id or name, e.g. com.apple.Notes or Notes) whose dictations may be ideas; empty allows all")
	minWords := fs.Int("min-words", DefaultMinWords, "minimum words after the trigger")
	sqlite3 := fs.String("sqlite3", DefaultSQLite3, "sqlite3 command used to read flow.sqlite (whitespace-separated)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "dibs ingest wisprflow: unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	if *db == "" {
		fmt.Fprintf(stderr, "dibs ingest wisprflow: --db is required on %s (no default Wispr Flow location)\n", goos)
		return 2
	}
	triggers, allow := ingest.SplitList(*trigger), ingest.SplitList(*apps)
	if len(triggers) == 0 && len(allow) == 0 {
		fmt.Fprintln(stderr, "dibs ingest wisprflow: set --trigger or --apps; importing every dictation would flood intake")
		return 2
	}
	in := &ingest.Ingester{Overlap: RelistOverlap}
	if code := shared.Configure(in, getenv, stderr); code != 0 {
		return code
	}
	in.Source = &Source{Path: *db, SQLite3: *sqlite3, Triggers: triggers, Apps: allow, MinWords: *minWords, Log: in.Log}
	if err := in.Run(ctx, shared.Interval()); err != nil {
		fmt.Fprintf(stderr, "dibs ingest wisprflow: %v\n", err)
		return 1
	}
	return 0
}
