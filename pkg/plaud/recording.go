package plaud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hivecommons/dibs/pkg/store"
)

// listPageSize is the list_files page size requested.
const listPageSize = 50

// truncatedMarker ends a description cut to fit the idea body cap.
const truncatedMarker = "\n\n_[transcript truncated to fit the Dibs idea size limit]_\n"

// errNotReady marks a recording with neither a note nor a transcript yet;
// it is retried on the next run.
var errNotReady = errors.New("no note or transcript available yet")

// Recording is one Plaud recording as listed by list_files.
type Recording struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	StartAt    time.Time
	DurationMS int64
}

// When is the best known recording time (zero if unknown).
func (r Recording) When() time.Time {
	if !r.StartAt.IsZero() {
		return r.StartAt
	}
	return r.CreatedAt
}

// Idea is what gets submitted to Dibs for one recording.
type Idea struct {
	Title       string
	Description string
	Tags        []string
}

// listRecordings pages through list_files from since until a page is short,
// adds nothing new, or limit recordings are collected. Recordings older than
// since are dropped client-side too, in case the server ignores date_from.
func listRecordings(ctx context.Context, tools Tools, since time.Time, limit int) ([]Recording, error) {
	seen := map[string]bool{}
	var out []Recording
	for page := 1; len(out) < limit; page++ {
		args := map[string]any{"page": page, "page_size": listPageSize}
		if !since.IsZero() {
			args["date_from"] = since.UTC().Format("2006-01-02")
		}
		text, err := tools.CallTool(ctx, "list_files", args)
		if err != nil {
			return nil, err
		}
		recs, err := decodeRecordings(text)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, r := range recs {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			if w := r.When(); !since.IsZero() && !w.IsZero() && w.Before(since) {
				continue
			}
			out = append(out, r)
			added++
			if len(out) >= limit {
				break
			}
		}
		if added == 0 || len(recs) < listPageSize {
			break
		}
	}
	return out, nil
}

// decodeRecordings accepts a JSON array of files or an object wrapping one
// under a common key, tolerating leading prose before the JSON.
func decodeRecordings(text string) ([]Recording, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		if i := strings.IndexAny(text, "[{"); i > 0 {
			return decodeRecordings(text[i:])
		}
		return nil, fmt.Errorf("plaud: list_files returned non-JSON output: %s", firstLine(text))
	}
	var items []any
	switch v := raw.(type) {
	case []any:
		items = v
	case map[string]any:
		for _, key := range []string{"files", "items", "data", "results", "recordings"} {
			if arr, ok := v[key].([]any); ok {
				items = arr
				break
			}
		}
		if items == nil && v["id"] != nil {
			items = []any{v}
		}
	}
	var out []Recording
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		r := Recording{
			ID:        stringField(m, "id", "file_id"),
			Name:      stringField(m, "name", "title", "filename"),
			CreatedAt: timeField(m, "created_at", "create_time"),
			StartAt:   timeField(m, "start_at", "start_time"),
		}
		if d, ok := m["duration"].(float64); ok {
			r.DurationMS = int64(d)
		}
		if r.ID != "" {
			out = append(out, r)
		}
	}
	return out, nil
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

func timeField(m map[string]any, keys ...string) time.Time {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
				if t, err := time.Parse(layout, strings.TrimSpace(v)); err == nil {
					return t.UTC()
				}
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

// renderText turns a get_note/get_transcript payload into plain text. JSON
// payloads are unwrapped (text-bearing fields, transcript segments); any
// other output is used as-is.
func renderText(text string) string {
	text = strings.TrimSpace(text)
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return text
	}
	return strings.TrimSpace(renderValue(raw))
}

func renderValue(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []any:
		lines := make([]string, 0, len(v))
		for _, it := range v {
			if line := strings.TrimSpace(renderValue(it)); line != "" {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	case map[string]any:
		for _, key := range []string{"markdown", "summary", "note", "content", "text", "transcript", "segments", "data"} {
			inner, ok := v[key]
			if !ok {
				continue
			}
			body := strings.TrimSpace(renderValue(inner))
			if body == "" {
				continue
			}
			if speaker := stringField(v, "speaker", "speaker_name"); speaker != "" {
				return speaker + ": " + body
			}
			return body
		}
	}
	return ""
}

// buildIdea fetches the note and transcript for r and renders the idea.
// A recording whose note and transcript both fail or are empty is reported
// as an error so it is retried later rather than marked as seen.
func buildIdea(ctx context.Context, tools Tools, r Recording, tags []string) (Idea, error) {
	args := map[string]any{"file_id": r.ID}
	note, noteErr := tools.CallTool(ctx, "get_note", args)
	transcript, trErr := tools.CallTool(ctx, "get_transcript", args)
	if noteErr != nil && trErr != nil {
		return Idea{}, trErr
	}
	note, transcript = renderText(note), renderText(transcript)
	if note == "" && transcript == "" {
		return Idea{}, errNotReady
	}
	return Idea{Title: ideaTitle(r), Description: ideaDescription(r, note, transcript), Tags: tags}, nil
}

func ideaTitle(r Recording) string {
	title := r.Name
	if title == "" {
		if w := r.When(); !w.IsZero() {
			title = "Plaud recording " + w.UTC().Format("2006-01-02 15:04 UTC")
		} else {
			title = "Plaud recording " + r.ID
		}
	}
	return capRunes(title, store.MaxTitleLen)
}

func ideaDescription(r Recording, note, transcript string) string {
	var b strings.Builder
	b.WriteString("_Voice note captured with Plaud")
	if w := r.When(); !w.IsZero() {
		fmt.Fprintf(&b, ", recorded %s", w.UTC().Format(time.RFC3339))
	}
	if r.DurationMS > 0 {
		fmt.Fprintf(&b, " (%s)", (time.Duration(r.DurationMS) * time.Millisecond).Round(time.Second))
	}
	fmt.Fprintf(&b, "; Plaud file %s._\n\n", r.ID)
	if note != "" {
		b.WriteString("## Summary\n\n" + note + "\n\n")
	}
	if transcript != "" {
		b.WriteString("## Transcript\n\n" + transcript + "\n")
	}
	return capBytes(b.String(), store.MaxBodyBytes)
}

// capBytes cuts s to at most limit bytes on a UTF-8 boundary, ending it with
// truncatedMarker when cut.
func capBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit - len(truncatedMarker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedMarker
}

func capRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
