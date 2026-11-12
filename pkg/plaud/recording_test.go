package plaud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hivecommons/dibs/pkg/store"
)

// fakeTools is an in-memory Plaud MCP session.
type fakeTools struct {
	pages       []string // list_files text per page; later pages are "[]"
	listErr     error
	notes       map[string]string
	transcripts map[string]string
	listArgs    []map[string]any
	closed      bool
}

func (f *fakeTools) CallTool(_ context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "list_files":
		f.listArgs = append(f.listArgs, args)
		if f.listErr != nil {
			return "", f.listErr
		}
		page := args["page"].(int)
		if page > len(f.pages) {
			return "[]", nil
		}
		return f.pages[page-1], nil
	case "get_note", "get_transcript":
		m := f.notes
		if name == "get_transcript" {
			m = f.transcripts
		}
		v, ok := m[args["file_id"].(string)]
		if !ok {
			return "", fmt.Errorf("%s: not found", name)
		}
		return v, nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

func (f *fakeTools) Close() error {
	f.closed = true
	return nil
}

func filesJSON(t *testing.T, files ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDecodeRecordingsShapes(t *testing.T) {
	ms := float64(time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).UnixMilli())
	sec := float64(time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC).Unix())
	cases := []struct {
		name string
		in   string
		ids  []string
	}{
		{"empty", "  ", nil},
		{"array", `[{"id":"a"},{"file_id":"b"},"junk",{"name":"no id"}]`, []string{"a", "b"}},
		{"wrapped", `{"data":[{"id":"c"}]}`, []string{"c"}},
		{"single", `{"id":"d","title":"T"}`, []string{"d"}},
		{"numeric id", `[{"id":42}]`, []string{"42"}},
		{"prose prefix", "Found 1 file:\n[{\"id\":\"e\"}]", []string{"e"}},
		{"unknown object", `{"count":0}`, nil},
	}
	for _, tc := range cases {
		recs, err := decodeRecordings(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var ids []string
		for _, r := range recs {
			ids = append(ids, r.ID)
		}
		if fmt.Sprint(ids) != fmt.Sprint(tc.ids) {
			t.Errorf("%s: ids = %v, want %v", tc.name, ids, tc.ids)
		}
	}
	recs, err := decodeRecordings(fmt.Sprintf(`[{"id":"t","filename":" f.mp3 ","created_at":%v,"start_time":%v,"duration":1500},{"id":"u","create_time":"2026-11-03 10:00:00","start_at":"2026-11-04"},{"id":"v","created_at":"2026-11-05T01:02:03.5Z","start_at":-1}]`, ms, sec))
	if err != nil {
		t.Fatal(err)
	}
	if r := recs[0]; r.Name != "f.mp3" || r.CreatedAt.UnixMilli() != int64(ms) || r.StartAt.Unix() != int64(sec) || r.DurationMS != 1500 {
		t.Errorf("numeric times = %+v", r)
	}
	if r := recs[1]; r.CreatedAt.Hour() != 10 || r.StartAt.Day() != 4 {
		t.Errorf("string times = %+v", r)
	}
	if r := recs[2]; r.CreatedAt.Second() != 3 || !r.StartAt.IsZero() || !r.When().Equal(r.CreatedAt) {
		t.Errorf("RFC3339 time / When fallback = %+v", r)
	}
	if _, err := decodeRecordings("not json at all"); err == nil {
		t.Fatal("non-JSON: want error")
	}
}

func TestRenderText(t *testing.T) {
	cases := map[string]string{
		"  plain words ":                           "plain words",
		`"a json string"`:                          "a json string",
		`{"summary":"Sum"}`:                        "Sum",
		`{"markdown":"","note":{"text":"Nested"}}`: "Nested",
		`{"segments":[{"speaker":"A","text":"hi"},{"speaker_name":"B","content":"yo"},{"other":1},"loose"]}`: "A: hi\nB: yo\nloose",
		`{"unknown":1}`: "",
		`42`:            "",
	}
	for in, want := range cases {
		if got := renderText(in); got != want {
			t.Errorf("renderText(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestIdeaTitle(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		r    Recording
		want string
	}{
		{Recording{ID: "x", Name: "Named"}, "Named"},
		{Recording{ID: "x", StartAt: at}, "Plaud recording 2026-11-10 09:30 UTC"},
		{Recording{ID: "x"}, "Plaud recording x"},
	}
	for _, tc := range cases {
		if got := ideaTitle(tc.r); got != tc.want {
			t.Errorf("ideaTitle(%+v) = %q, want %q", tc.r, got, tc.want)
		}
	}
	long := ideaTitle(Recording{ID: "x", Name: strings.Repeat("é", store.MaxTitleLen+5)})
	if utf8.RuneCountInString(long) != store.MaxTitleLen {
		t.Fatalf("long title has %d runes, want %d", utf8.RuneCountInString(long), store.MaxTitleLen)
	}
}

func TestIdeaDescription(t *testing.T) {
	r := Recording{ID: "rec-9", StartAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), DurationMS: 61400}
	got := ideaDescription(r, "Summary text", "Transcript text")
	for _, want := range []string{"recorded 2026-11-10T09:00:00Z", "(1m1s)", "Plaud file rec-9", "## Summary\n\nSummary text", "## Transcript\n\nTranscript text"} {
		if !strings.Contains(got, want) {
			t.Errorf("description missing %q:\n%s", want, got)
		}
	}
	bare := ideaDescription(Recording{ID: "z"}, "", "only transcript")
	if strings.Contains(bare, "recorded") || strings.Contains(bare, "## Summary") || !strings.Contains(bare, "only transcript") {
		t.Errorf("bare description = %q", bare)
	}
	huge := ideaDescription(r, "", strings.Repeat("ü", store.MaxBodyBytes))
	if len(huge) > store.MaxBodyBytes || !utf8.ValidString(huge) || !strings.HasSuffix(huge, truncatedMarker) {
		t.Fatalf("huge description: %d bytes, valid=%v", len(huge), utf8.ValidString(huge))
	}
}

func TestListRecordingsPaginates(t *testing.T) {
	var page1, page2 []map[string]any
	for i := 0; i < listPageSize; i++ {
		page1 = append(page1, map[string]any{"id": fmt.Sprintf("p1-%d", i), "start_at": "2026-11-10T00:00:00Z"})
	}
	page2 = append(page2,
		map[string]any{"id": "p1-0"},
		map[string]any{"id": "old", "start_at": "2026-10-01T00:00:00Z"},
		map[string]any{"id": "p2-new"},
	)
	tools := &fakeTools{pages: []string{filesJSON(t, page1...), filesJSON(t, page2...)}}
	since := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	recs, err := listRecordings(context.Background(), tools, since, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != listPageSize+1 || recs[len(recs)-1].ID != "p2-new" {
		t.Fatalf("got %d recordings (last %q), want page 1 plus p2-new", len(recs), recs[len(recs)-1].ID)
	}
	if len(tools.listArgs) != 2 || tools.listArgs[0]["date_from"] != "2026-11-09" {
		t.Fatalf("list args = %v", tools.listArgs)
	}

	capped, err := listRecordings(context.Background(), &fakeTools{pages: []string{filesJSON(t, page1...)}}, time.Time{}, 3)
	if err != nil || len(capped) != 3 {
		t.Fatalf("capped = %d, %v; want 3", len(capped), err)
	}
}

func TestListRecordingsErrors(t *testing.T) {
	if _, err := listRecordings(context.Background(), &fakeTools{listErr: errors.New("401")}, time.Time{}, 10); err == nil {
		t.Fatal("list error: want error")
	}
	if _, err := listRecordings(context.Background(), &fakeTools{pages: []string{"garbage"}}, time.Time{}, 10); err == nil {
		t.Fatal("decode error: want error")
	}
}

func TestBuildIdea(t *testing.T) {
	r := Recording{ID: "a", Name: "Idea"}
	tools := &fakeTools{notes: map[string]string{"a": "note", "empty": " "}, transcripts: map[string]string{"empty": "[]"}}
	idea, err := buildIdea(context.Background(), tools, r, []string{"plaud"})
	if err != nil || idea.Title != "Idea" || !strings.Contains(idea.Description, "note") || strings.Contains(idea.Description, "## Transcript") || idea.Tags[0] != "plaud" {
		t.Fatalf("note only: %+v, %v", idea, err)
	}
	if _, err := buildIdea(context.Background(), tools, Recording{ID: "missing"}, nil); err == nil || errors.Is(err, errNotReady) {
		t.Fatalf("both calls failing: err = %v, want the tool error", err)
	}
	if _, err := buildIdea(context.Background(), tools, Recording{ID: "empty"}, nil); !errors.Is(err, errNotReady) {
		t.Fatalf("empty note and transcript: err = %v, want errNotReady", err)
	}
}
