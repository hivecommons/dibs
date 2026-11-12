package wisprflow

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Dictation is one Wispr Flow history row, normalised from SQLite or an
// export file.
type Dictation struct {
	ID   string
	Text string
	At   time.Time
	// App is the bundle id (or name) of the app dictated into.
	App string
}

// decodeJSON accepts the sqlite3 -json array, or any JSON array / wrapped
// object list of dictations as written by community exporters.
func decodeJSON(data []byte) ([]Dictation, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, nil
	}
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("wisprflow: invalid JSON: %s", firstLine(text))
	}
	var items []any
	switch v := raw.(type) {
	case []any:
		items = v
	case map[string]any:
		for _, key := range []string{"dictations", "history", "items", "data", "results", "transcripts"} {
			if arr, ok := v[key].([]any); ok {
				items = arr
				break
			}
		}
		if items == nil {
			items = []any{v}
		}
	}
	var out []Dictation
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if d, ok := fromMap(m); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// fromMap normalises one dictation object; SQLite and the exporters name
// the same fields differently. The text prefers the user's edit, then
// Flow's formatted text, then the raw ASR text.
func fromMap(m map[string]any) (Dictation, bool) {
	d := Dictation{
		ID:   stringField(m, "id", "transcriptEntityId", "transcript_id", "uuid"),
		Text: stringField(m, "text", "editedText", "edited_text", "formattedText", "formatted_text", "content", "asrText", "asr_text"),
		At:   timeField(m, "at", "timestamp", "created_at", "createdAt", "date"),
		App:  stringField(m, "app", "application", "bundle_id"),
	}
	if d.ID == "" || d.Text == "" || d.At.IsZero() {
		return d, false
	}
	return d, true
}

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}

// timeField reads the first parseable time among keys: a date string (see
// parseTime) or epoch seconds / milliseconds.
func timeField(m map[string]any, keys ...string) time.Time {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if t := parseTime(v); !t.IsZero() {
				return t
			}
		case float64:
			if v > 1e12 {
				return time.UnixMilli(int64(v)).UTC()
			}
			if v > 0 {
				return time.Unix(int64(v), 0).UTC()
			}
		}
	}
	return time.Time{}
}

// parseTime handles Sequelize's "2026-11-11 14:05:09.123 +00:00" (what
// flow.sqlite stores) and the common ISO shapes; times without a zone are
// UTC.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return timeField(map[string]any{"t": float64(n)}, "t")
	}
	return time.Time{}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
