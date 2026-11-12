package wisprflow

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// maxExportFileBytes caps one export file read.
const maxExportFileBytes = 64 << 20

// filenameTimeRe pulls a date (and optional time) from an exported markdown
// filename such as 2026-11-11-1405-slack.md or 2026-11-11T14-05-09.md.
var filenameTimeRe = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})(?:[T _-](\d{2})[-:h]?(\d{2})(?:[-:m]?(\d{2}))?)?`)

// readExportDir walks an export folder (a community exporter's output or a
// hand-made dump) and returns the dictations newer than since, deduped on
// id. Supported files: .json (array or wrapped object), .ndjson / .jsonl
// (one object per line), .csv (header row) and .md / .markdown / .txt (one
// dictation per file, optional YAML-style front matter). Unreadable files
// are reported together as one error while the readable ones are still
// returned.
func readExportDir(dir string, since time.Time) ([]Dictation, error) {
	var out []Dictation
	var problems []string
	seen := map[string]bool{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, walkErr))
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		rows, err := readExportFile(dir, path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(path), err))
		}
		for _, r := range rows {
			if !r.At.After(since) || seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			out = append(out, r)
		}
		return nil
	})
	if len(problems) > 0 {
		return out, fmt.Errorf("wisprflow: %d export file(s) skipped: %s", len(problems), strings.Join(problems, "; "))
	}
	return out, nil
}

// readExportFile dispatches on extension; unknown extensions are ignored.
func readExportFile(root, path string) ([]Dictation, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json", ".ndjson", ".jsonl", ".csv", ".md", ".markdown", ".txt":
	default:
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxExportFileBytes {
		return nil, fmt.Errorf("file larger than %d bytes", maxExportFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch ext {
	case ".json":
		return decodeJSON(data)
	case ".ndjson", ".jsonl":
		return decodeNDJSON(data)
	case ".csv":
		return decodeCSV(data)
	default:
		rel, _ := filepath.Rel(root, path)
		d, ok := decodeMarkdown(rel, data, info.ModTime())
		if !ok {
			return nil, nil
		}
		return []Dictation{d}, nil
	}
}

func decodeNDJSON(data []byte) ([]Dictation, error) {
	var out []Dictation
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), maxExportFileBytes)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			return out, fmt.Errorf("line %d: %v", line, err)
		}
		if d, ok := fromMap(m); ok {
			out = append(out, d)
		}
	}
	return out, sc.Err()
}

// decodeCSV maps header names onto the keys fromMap accepts.
func decodeCSV(data []byte) ([]Dictation, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil || len(records) == 0 {
		return nil, err
	}
	header := records[0]
	for i := range header {
		header[i] = strings.TrimSpace(strings.TrimPrefix(header[i], "\ufeff"))
	}
	var out []Dictation
	for _, rec := range records[1:] {
		m := make(map[string]any, len(header))
		for i, h := range header {
			if i < len(rec) && h != "" {
				m[h] = rec[i]
			}
		}
		if d, ok := fromMap(m); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// decodeMarkdown reads one dictation per markdown file. Front matter keys
// id / transcriptEntityId, timestamp / dictated / date / created and app are
// honoured; otherwise the id is the relative path and the time comes from
// the filename or, failing that, the file's mtime.
func decodeMarkdown(rel string, data []byte, modTime time.Time) (Dictation, bool) {
	body := strings.ReplaceAll(string(data), "\r\n", "\n")
	meta := map[string]string{}
	if strings.HasPrefix(body, "---\n") {
		if end := strings.Index(body[4:], "\n---"); end >= 0 {
			for _, line := range strings.Split(body[4:4+end], "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				meta[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
			body = body[4+end+4:]
		}
	}
	body = strings.TrimSpace(body)
	// A leading heading is an exporter's title, not dictated text.
	if strings.HasPrefix(body, "# ") {
		_, rest, _ := strings.Cut(body, "\n")
		body = strings.TrimSpace(rest)
	}
	d := Dictation{Text: body, App: meta["app"]}
	d.ID = meta["id"]
	if d.ID == "" {
		d.ID = meta["transcriptentityid"]
	}
	if d.ID == "" {
		d.ID = strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
	}
	for _, k := range []string{"timestamp", "dictated", "date", "created", "created_at", "createdat"} {
		if v := meta[k]; v != "" {
			d.At = parseTime(v)
			if d.At.IsZero() {
				d.At, _ = timeFromName(v)
			}
			break
		}
	}
	if d.At.IsZero() {
		if t, ok := timeFromName(filepath.Base(rel)); ok {
			d.At = t
		} else {
			d.At = modTime.UTC()
		}
	}
	return d, d.Text != ""
}

// timeFromName extracts a UTC time from a filename-style date stamp.
func timeFromName(name string) (time.Time, bool) {
	m := filenameTimeRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	stamp := m[1]
	layout := "2006-01-02"
	if m[2] != "" {
		stamp += " " + m[2] + ":" + m[3]
		layout = "2006-01-02 15:04"
		if m[4] != "" {
			stamp += ":" + m[4]
			layout = "2006-01-02 15:04:05"
		}
	}
	t, err := time.Parse(layout, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
