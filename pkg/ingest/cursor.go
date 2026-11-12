package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/hivecommons/dibs/pkg/fsutil"
)

// seenRetention is how long a submitted item id is remembered past the
// newest item; listing only reaches Ingester.Overlap behind it.
const seenRetention = 30 * 24 * time.Hour

// Cursor is the persisted ingest state: which items were already submitted
// (dedupe on the source's id), the newest item time (the next listing starts
// there, minus Ingester.Overlap), and recent submission times (rate budget).
type Cursor struct {
	Newest    time.Time            `json:"newest,omitzero"`
	Seen      map[string]time.Time `json:"seen"`
	Submitted []time.Time          `json:"submitted,omitempty"`
}

// LoadCursor reads path; a missing file is an empty cursor.
func LoadCursor(path string) (*Cursor, error) {
	c := &Cursor{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("ingest: reading cursor: %w", err)
	default:
		if err := json.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("ingest: parsing cursor %s: %w", path, err)
		}
	}
	if c.Seen == nil {
		c.Seen = map[string]time.Time{}
	}
	return c, nil
}

// Save writes the cursor atomically (mode 0600), creating its directory.
func (c *Cursor) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("ingest: creating cursor directory: %w", err)
	}
	if err := fsutil.AtomicWriteJSON(path, c); err != nil {
		return fmt.Errorf("ingest: writing cursor: %w", err)
	}
	return nil
}

// markSubmitted records item id, captured at when, as submitted at now.
func (c *Cursor) markSubmitted(id string, when, now time.Time) {
	if when.IsZero() || when.After(now) {
		when = now
	}
	c.Seen[id] = when
	c.advance(when, now)
	c.Submitted = append(c.Submitted, now)
}

// advance moves Newest forward to when (clamped to now) and reports whether
// it moved.
func (c *Cursor) advance(when, now time.Time) bool {
	if when.After(now) {
		when = now
	}
	if !when.After(c.Newest) {
		return false
	}
	c.Newest = when
	return true
}

// prune forgets submission times older than a day and seen ids well behind
// the newest item, so the file stays small.
func (c *Cursor) prune(now time.Time) {
	kept := c.Submitted[:0]
	for _, t := range c.Submitted {
		if now.Sub(t) < 24*time.Hour {
			kept = append(kept, t)
		}
	}
	c.Submitted = kept
	for id, t := range c.Seen {
		if c.Newest.Sub(t) > seenRetention {
			delete(c.Seen, id)
		}
	}
}

// allow reports whether another submission fits the per-hour and per-day
// budgets (a budget <= 0 is unlimited).
func (c *Cursor) allow(now time.Time, perHour, perDay int) bool {
	hour, day := 0, 0
	for _, t := range c.Submitted {
		if now.Sub(t) < time.Hour {
			hour++
		}
		if now.Sub(t) < 24*time.Hour {
			day++
		}
	}
	return (perHour <= 0 || hour < perHour) && (perDay <= 0 || day < perDay)
}
