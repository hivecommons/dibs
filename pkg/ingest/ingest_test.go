package ingest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

type fakeItem struct {
	id   string
	when time.Time
	err  error
}

func (i fakeItem) ID() string      { return i.id }
func (i fakeItem) When() time.Time { return i.when }
func (i fakeItem) Idea(context.Context) (Idea, error) {
	return Idea{Title: i.id, Description: "body of " + i.id, Tags: []string{"ignored"}}, i.err
}

// fakeSource lists its items and remembers each since.
type fakeSource struct {
	items  []Item
	since  []time.Time
	closed bool
	onList func()
}

func (s *fakeSource) List(_ context.Context, since time.Time) ([]Item, error) {
	s.since = append(s.since, since)
	if s.onList != nil {
		s.onList()
	}
	return s.items, nil
}

func (s *fakeSource) Close() error {
	s.closed = true
	return nil
}

type watermarkSource struct {
	fakeSource
	mark time.Time
}

func (s *watermarkSource) Watermark() time.Time { return s.mark }

type recordSink struct {
	ideas []Idea
	err   error
}

func (s *recordSink) Submit(_ context.Context, idea Idea) error {
	if s.err != nil {
		return s.err
	}
	s.ideas = append(s.ideas, idea)
	return nil
}

func newTestIngester(t *testing.T, src Source, sink Sink) *Ingester {
	t.Helper()
	return &Ingester{
		Name:       "test",
		Source:     src,
		Sink:       sink,
		CursorPath: filepath.Join(t.TempDir(), "cursor.json"),
		Tags:       []string{"voice-note"},
		Overlap:    2 * time.Hour,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        func() time.Time { return testNow },
	}
}

func TestRunOnceUsesStaticSourceAndOverridesTags(t *testing.T) {
	src := &fakeSource{items: []Item{
		fakeItem{id: "b", when: testNow.Add(-time.Hour)},
		fakeItem{id: "a", when: testNow.Add(-3 * time.Hour)},
	}}
	sink := &recordSink{}
	in := newTestIngester(t, src, sink)
	res, err := in.RunOnce(context.Background())
	if err != nil || res.Submitted != 2 || len(sink.ideas) != 2 || sink.ideas[0].Title != "a" || !src.closed {
		t.Fatalf("res=%+v err=%v ideas=%+v closed=%v", res, err, sink.ideas, src.closed)
	}
	if strings.Join(sink.ideas[0].Tags, ",") != "voice-note" {
		t.Fatalf("tags = %v, want the ingester's tags", sink.ideas[0].Tags)
	}
	if want := testNow.Add(-DefaultSinceDays * 24 * time.Hour); !src.since[0].Equal(want) {
		t.Fatalf("first since = %v, want %v", src.since[0], want)
	}
	if _, err := in.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := testNow.Add(-3 * time.Hour); !src.since[1].Equal(want) {
		t.Fatalf("second since = %v, want newest minus Overlap (%v)", src.since[1], want)
	}
}

func TestRunOnceWithoutSource(t *testing.T) {
	in := newTestIngester(t, nil, &recordSink{})
	if _, err := in.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "no source") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunOnceAdvancesWatermark(t *testing.T) {
	mark := testNow.Add(-30 * time.Minute)
	src := &watermarkSource{mark: mark}
	in := newTestIngester(t, src, &recordSink{})
	if _, err := in.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cur, err := LoadCursor(in.CursorPath)
	if err != nil || !cur.Newest.Equal(mark) {
		t.Fatalf("newest = %v, %v; want the watermark %v", cur.Newest, err, mark)
	}
	if _, err := in.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := mark.Add(-2 * time.Hour); !src.since[1].Equal(want) {
		t.Fatalf("since after watermark = %v, want %v", src.since[1], want)
	}

	in.CursorPath = filepath.Join(t.TempDir(), "c.json")
	src.onList = func() { _ = os.MkdirAll(in.CursorPath, 0o700) }
	if _, err := in.RunOnce(context.Background()); err == nil {
		t.Fatal("unwritable cursor: want error")
	}
}

func TestRunOnceKeepsWatermarkWhenBatchIncomplete(t *testing.T) {
	mark := testNow.Add(-10 * time.Minute)
	early := testNow.Add(-5 * time.Hour)
	cases := map[string]func(*Ingester, *watermarkSource){
		"dry run": func(in *Ingester, _ *watermarkSource) { in.DryRun = true },
		"per-run cap": func(in *Ingester, s *watermarkSource) {
			in.MaxPerRun = 1
			s.items = append(s.items, fakeItem{id: "late", when: early.Add(time.Hour)})
		},
		"skipped": func(_ *Ingester, s *watermarkSource) {
			s.items = append(s.items, fakeItem{id: "broken", when: early.Add(time.Hour), err: errors.New("not ready")})
		},
		"rate limited": func(in *Ingester, _ *watermarkSource) { in.Sink = &recordSink{err: ErrRateLimited} },
	}
	for name, tweak := range cases {
		src := &watermarkSource{fakeSource: fakeSource{items: []Item{fakeItem{id: "early", when: early}}}, mark: mark}
		in := newTestIngester(t, src, &recordSink{})
		tweak(in, src)
		if _, err := in.RunOnce(context.Background()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cur, err := LoadCursor(in.CursorPath)
		if err != nil || cur.Newest.Equal(mark) {
			t.Errorf("%s: newest = %v, %v; the watermark must not move past an unfinished batch", name, cur.Newest, err)
		}
	}
}

func TestRunDefaultsAndLoop(t *testing.T) {
	in := &Ingester{Source: &fakeSource{}, CursorPath: filepath.Join(t.TempDir(), "c.json")}
	if in.now().IsZero() || in.log() == nil || in.name() != "ingest" {
		t.Fatal("defaults not applied")
	}
	if err := in.Run(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	in.Log = slog.New(slog.NewTextHandler(&buf, nil))
	in.Name = "wisprflow"
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	in.Open = func(context.Context) (Source, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("db locked")
		}
		cancel()
		return &fakeSource{}, nil
	}
	if err := in.Run(ctx, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "db locked") || !strings.Contains(buf.String(), "wisprflow ingest run done") {
		t.Fatalf("log = %s", buf.String())
	}
}
