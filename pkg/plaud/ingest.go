package plaud

import (
	"context"
	"log/slog"
	"time"

	"github.com/hivecommons/dibs/pkg/ingest"
)

const (
	// relistOverlap re-lists this far behind the cursor so recordings whose
	// transcript finished late are still picked up.
	relistOverlap = 24 * time.Hour
	// DefaultSinceDays bounds how far back the first run looks.
	DefaultSinceDays = ingest.DefaultSinceDays
	// DefaultMaxPerHour and DefaultMaxPerDay match the intake budget per
	// identity (10/h, 30/24h).
	DefaultMaxPerHour = ingest.DefaultMaxPerHour
	DefaultMaxPerDay  = ingest.DefaultMaxPerDay
	// maxFilesPerRun caps recordings listed per run.
	maxFilesPerRun = 200
)

// ErrRateLimited reports that Dibs (or a proxy in front of it) answered
// 429; the batch stops and resumes on the next run.
var ErrRateLimited = ingest.ErrRateLimited

type (
	// Idea is what gets submitted to Dibs for one recording.
	Idea = ingest.Idea
	// Sink submits one idea to Dibs.
	Sink = ingest.Sink
	// DibsSink submits ideas with the Dibs MCP submit_idea tool.
	DibsSink = ingest.DibsSink
	// Result summarizes one run.
	Result = ingest.Result
	// Cursor is the persisted ingest state (see ingest.Cursor).
	Cursor = ingest.Cursor
)

// LoadCursor reads path; a missing file is an empty cursor.
func LoadCursor(path string) (*Cursor, error) { return ingest.LoadCursor(path) }

// Ingester moves new Plaud recordings into Dibs. It is ingest.Ingester
// with a Plaud MCP session as the source.
type Ingester struct {
	// Dial opens a Plaud MCP session; it is called once per run.
	Dial       func(ctx context.Context) (Tools, error)
	Sink       Sink
	CursorPath string
	Tags       []string
	SinceDays  int
	MaxPerHour int
	MaxPerDay  int
	// MaxPerRun caps recordings handled in one run (0 = no cap).
	MaxPerRun int
	// DryRun builds and logs each idea without submitting it or advancing
	// the cursor.
	DryRun bool
	Log    *slog.Logger
	Now    func() time.Time
}

func (in *Ingester) engine() *ingest.Ingester {
	return &ingest.Ingester{
		Name:       "plaud",
		Open:       openSource(in.Dial),
		Sink:       in.Sink,
		CursorPath: in.CursorPath,
		Tags:       in.Tags,
		SinceDays:  in.SinceDays,
		Overlap:    relistOverlap,
		MaxPerHour: in.MaxPerHour,
		MaxPerDay:  in.MaxPerDay,
		MaxPerRun:  in.MaxPerRun,
		DryRun:     in.DryRun,
		Log:        in.Log,
		Now:        in.Now,
	}
}

// RunOnce lists recordings since the cursor and submits each unseen one,
// oldest first (see ingest.Ingester.RunOnce).
func (in *Ingester) RunOnce(ctx context.Context) (Result, error) {
	return in.engine().RunOnce(ctx)
}

// Run calls RunOnce, then every interval until ctx is done (see
// ingest.Ingester.Run).
func (in *Ingester) Run(ctx context.Context, interval time.Duration) error {
	return in.engine().Run(ctx, interval)
}

// openSource adapts a Plaud MCP dialer to ingest.Ingester.Open.
func openSource(dial func(ctx context.Context) (Tools, error)) func(ctx context.Context) (ingest.Source, error) {
	return func(ctx context.Context) (ingest.Source, error) {
		tools, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		return &source{tools: tools}, nil
	}
}

// source lists Plaud recordings through one MCP session.
type source struct {
	tools Tools
}

func (s *source) List(ctx context.Context, since time.Time) ([]ingest.Item, error) {
	recs, err := listRecordings(ctx, s.tools, since, maxFilesPerRun)
	if err != nil {
		return nil, err
	}
	items := make([]ingest.Item, len(recs))
	for i, r := range recs {
		items[i] = &item{rec: r, tools: s.tools}
	}
	return items, nil
}

func (s *source) Close() error { return s.tools.Close() }

// item is one recording; its note and transcript are fetched only when the
// recording is new.
type item struct {
	rec   Recording
	tools Tools
}

func (it *item) ID() string      { return it.rec.ID }
func (it *item) When() time.Time { return it.rec.When() }
func (it *item) Idea(ctx context.Context) (Idea, error) {
	return buildIdea(ctx, it.tools, it.rec, nil)
}
