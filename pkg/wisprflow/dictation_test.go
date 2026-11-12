package wisprflow

import (
	"testing"
	"time"
)

func TestDecodeJSONShapes(t *testing.T) {
	cases := map[string]int{
		"":   0,
		"42": 0,
		`[1, "x", {"id":"a","text":"t","at":"2026-11-11T09:00:00Z"}]`:              1,
		`{"history":[{"id":"a","text":"t","at":"2026-11-11T09:00:00Z"}]}`:          1,
		`{"id":"a","text":"single object","at":"2026-11-11T09:00:00Z"}`:            1,
		`[{"id":"a","text":"   ","at":"2026-11-11T09:00:00Z"}]`:                    0,
		`[{"id":"a","text":"no time"}]`:                                            0,
		`[{"uuid":7,"asrText":"numeric id","timestamp":1794474000}]`:               1,
		`[{"id":"a","text":"bad time","at":"tomorrow","date":"2026-11-11"}]`:       1,
		`[{"id":"a","text":"epoch string","timestamp":"1794474000"}]`:              1,
		`[{"id":"a","text":"zero epoch","timestamp":0,"date":"2026-11-11 09:00"}]`: 1,
	}
	for in, want := range cases {
		got, err := decodeJSON([]byte(in))
		if err != nil || len(got) != want {
			t.Errorf("decodeJSON(%s) = %+v, %v; want %d", in, got, err, want)
		}
	}
	got, _ := decodeJSON([]byte(`[{"uuid":7,"asrText":"numeric id","timestamp":1794474000}]`))
	if got[0].ID != "7" || !got[0].At.Equal(time.Unix(1794474000, 0).UTC()) {
		t.Fatalf("numeric fields = %+v", got[0])
	}
	if _, err := decodeJSON([]byte("{")); err == nil {
		t.Fatal("invalid JSON: want error")
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 11, 11, 9, 5, 9, 123000000, time.UTC)
	for _, in := range []string{
		"2026-11-11 09:05:09.123 +00:00",
		"2026-11-11 10:05:09.123 +01:00",
		"2026-11-11 09:05:09.123Z",
		"2026-11-11 09:05:09.123",
		"2026-11-11T09:05:09.123Z",
		"2026-11-11T09:05:09.123",
	} {
		if got := parseTime(in); !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"", "  ", "yesterday"} {
		if got := parseTime(in); !got.IsZero() {
			t.Errorf("parseTime(%q) = %v, want zero", in, got)
		}
	}
	if got := parseTime("1794474000000"); !got.Equal(time.UnixMilli(1794474000000).UTC()) {
		t.Errorf("epoch ms string = %v", got)
	}
}
