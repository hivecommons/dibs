// Package ingest moves items captured outside Dibs (voice recordings,
// dictations) into Dibs as private draft ideas. A Source lists items since
// a time; the Ingester dedupes them against a Cursor file, applies a local
// submission budget, and submits each new one through a Sink (DibsSink, the
// Dibs MCP submit_idea tool).
package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"time"
)

const (
	// DefaultSinceDays bounds how far back the first run looks.
	DefaultSinceDays = 7
	// DefaultMaxPerHour and DefaultMaxPerDay match the intake budget per
	// identity (10/h, 30/24h).
	DefaultMaxPerHour = 10
	DefaultMaxPerDay  = 30
)

// ErrRateLimited reports that Dibs (or a proxy in front of it) answered
// 429; the batch stops and resumes on the next run.
var ErrRateLimited = errors.New("dibs: rate limited (HTTP 429)")

// Idea is what gets submitted to Dibs for one item.
type Idea struct {
	Title       string
	Description string
	Tags        []string
}

// Item is one candidate idea listed by a Source.
type Item interface {
	// ID is the source's stable id, used to dedupe across runs.
	ID() string
	// When is the item's capture time (zero if unknown).
	When() time.Time
	// Idea renders the idea; an error skips the item for this run only.
	Idea(ctx context.Context) (Idea, error)
}

// Source lists items captured at or after since. A Source that also
// implements io.Closer is closed at the end of each run.
type Source interface {
	List(ctx context.Context, since time.Time) ([]Item, error)
}

// Watermarker is an optional Source extension for sources that read and
// drop items that are not ideas (most Wispr Flow dictations). Watermark
// reports the newest capture time the last List call read, so the cursor
// moves past dropped items instead of re-reading them every run.
type Watermarker interface {
	Watermark() time.Time
}

// Sink submits one idea to Dibs.
type Sink interface {
	Submit(ctx context.Context, idea Idea) error
}

// Ingester moves new items from a Source into Dibs.
type Ingester struct {
	// Name labels log lines, e.g. "plaud".
	Name string
	// Open returns the Source for one run; when nil, Source is used.
	Open   func(ctx context.Context) (Source, error)
	Source Source
	Sink   Sink
	// CursorPath is the JSON state file (see Cursor).
	CursorPath string
	// Tags replace the tags of every idea.
	Tags      []string
	SinceDays int
	// Overlap re-lists this far behind the cursor, for items that were not
	// ready (or not final) when they were first listed.
	Overlap    time.Duration
	MaxPerHour int
	MaxPerDay  int
	// MaxPerRun caps items handled in one run (0 = no cap).
	MaxPerRun int
	// DryRun builds and logs each idea without submitting it or advancing
	// the cursor.
	DryRun bool
	Log    *slog.Logger
	Now    func() time.Time
}

// Result summarizes one run.
type Result struct {
	Submitted int
	Skipped   int
	// Planned counts ideas that a dry run would have submitted.
	Planned int
	// BudgetExhausted is set when the local --max-per-hour/--max-per-day
	// budget or the --max per-run cap stopped the batch.
	BudgetExhausted bool
	// RateLimited is set when Dibs answered HTTP 429.
	RateLimited bool
}

func (in *Ingester) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in *Ingester) log() *slog.Logger {
	if in.Log != nil {
		return in.Log
	}
	return slog.Default()
}

func (in *Ingester) name() string {
	if in.Name != "" {
		return in.Name
	}
	return "ingest"
}

func (in *Ingester) open(ctx context.Context) (Source, error) {
	if in.Open != nil {
		return in.Open(ctx)
	}
	if in.Source == nil {
		return nil, errors.New("ingest: no source configured")
	}
	return in.Source, nil
}

// RunOnce lists items since the cursor and submits each unseen one, oldest
// first. The cursor is saved after every submission, so a crash never
// resubmits. A 429 or an exhausted local budget stops the batch without
// error; the rest is picked up next run.
func (in *Ingester) RunOnce(ctx context.Context) (Result, error) {
	var res Result
	cur, err := LoadCursor(in.CursorPath)
	if err != nil {
		return res, err
	}
	now := in.now()
	cur.prune(now)
	src, err := in.open(ctx)
	if err != nil {
		return res, err
	}
	if c, ok := src.(io.Closer); ok {
		defer c.Close()
	}

	since := cur.Newest.Add(-in.Overlap)
	if cur.Newest.IsZero() {
		days := in.SinceDays
		if days <= 0 {
			days = DefaultSinceDays
		}
		since = now.Add(-time.Duration(days) * 24 * time.Hour)
	}
	items, err := src.List(ctx, since)
	if err != nil {
		return res, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].When().Before(items[j].When()) })
	name := in.name()
	stopped := false
	for _, it := range items {
		id := it.ID()
		if _, ok := cur.Seen[id]; ok {
			continue
		}
		if in.MaxPerRun > 0 && res.Submitted+res.Planned >= in.MaxPerRun {
			in.log().Info(name+" ingest per-run cap reached; resuming next run", "max", in.MaxPerRun)
			res.BudgetExhausted = true
			stopped = true
			break
		}
		if !cur.allow(now, in.MaxPerHour, in.MaxPerDay) {
			in.log().Info(name+" ingest budget reached; resuming next run", "per_hour", in.MaxPerHour, "per_day", in.MaxPerDay)
			res.BudgetExhausted = true
			stopped = true
			break
		}
		idea, err := it.Idea(ctx)
		if err != nil {
			in.log().Warn(name+" item skipped", "id", id, "err", err)
			res.Skipped++
			continue
		}
		idea.Tags = in.Tags
		if in.DryRun {
			res.Planned++
			in.log().Info(name+" item would be submitted (dry run)", "id", id, "title", idea.Title, "tags", idea.Tags, "description_bytes", len(idea.Description))
			continue
		}
		if err := in.Sink.Submit(ctx, idea); err != nil {
			if errors.Is(err, ErrRateLimited) {
				in.log().Warn("dibs rate limited the submission; resuming next run", "id", id)
				res.RateLimited = true
				stopped = true
				break
			}
			return res, err
		}
		cur.markSubmitted(id, it.When(), now)
		if err := cur.Save(in.CursorPath); err != nil {
			return res, err
		}
		res.Submitted++
		in.log().Info(name+" item submitted", "id", id, "title", idea.Title)
	}
	if w, ok := src.(Watermarker); ok && !stopped && !in.DryRun && res.Skipped == 0 {
		if cur.advance(w.Watermark(), now) {
			if err := cur.Save(in.CursorPath); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

// Run calls RunOnce, then every interval until ctx is done. With
// interval <= 0 it runs once and returns that run's error. In loop mode a
// failed run is logged and retried on the next tick.
func (in *Ingester) Run(ctx context.Context, interval time.Duration) error {
	name := in.name()
	for {
		res, err := in.RunOnce(ctx)
		if interval <= 0 {
			if err == nil {
				in.log().Info(name+" ingest done", "submitted", res.Submitted, "planned", res.Planned, "skipped", res.Skipped, "budget_exhausted", res.BudgetExhausted, "rate_limited", res.RateLimited)
			}
			return err
		}
		if err != nil {
			in.log().Warn(name+" ingest run failed", "err", err)
		} else {
			in.log().Info(name+" ingest run done", "submitted", res.Submitted, "planned", res.Planned, "skipped", res.Skipped, "budget_exhausted", res.BudgetExhausted, "rate_limited", res.RateLimited)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}
