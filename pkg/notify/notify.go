// Package notify is the lightweight in-app notification feed (the bell in
// the navbar): new match found, offer received, offer accepted/declined,
// issue opened. JSON-file backed with the same atomic temp+rename style as
// the idea store; per-user feeds are capped so the file can't grow forever.
package notify

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Kind values.
const (
	KindMatch    = "match"    // new match found
	KindOffer    = "offer"    // offer received (repo owner)
	KindAccepted = "accepted" // offer accepted (ideator)
	KindDeclined = "declined" // offer declined (ideator)
	KindIssue    = "issue"    // settled: issue opened (ideator)
)

// MaxPerUser caps each user's retained notifications (oldest dropped).
const MaxPerUser = 100

// Notification is one feed entry for one user.
type Notification struct {
	ID        string    `json:"id"`
	User      string    `json:"user"` // recipient identity key
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	IdeaID    string    `json:"ideaID,omitempty"`
	RepoID    string    `json:"repoID,omitempty"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store is a mutex-guarded JSON file store at dir/notifications.json.
type Store struct {
	mu   sync.RWMutex
	path string
	byID map[string]*Notification
}

// New opens (creating if needed) a notification store under dir.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("notify: creating data dir: %w", err)
	}
	s := &Store{path: filepath.Join(dir, "notifications.json"), byID: map[string]*Notification{}}
	raw, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// fresh store
	case err != nil:
		return nil, fmt.Errorf("notify: reading notifications.json: %w", err)
	default:
		var all []Notification
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, fmt.Errorf("notify: corrupt notifications.json: %w", err)
		}
		for i := range all {
			n := all[i]
			s.byID[n.ID] = &n
		}
	}
	return s, nil
}

func newID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("notify: generating id: %w", err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

func (s *Store) persistLocked() error {
	all := make([]Notification, 0, len(s.byID))
	for _, n := range s.byID {
		all = append(all, *n)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return fmt.Errorf("notify: marshaling: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tmp-notify-*")
	if err != nil {
		return fmt.Errorf("notify: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("notify: writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("notify: closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("notify: renaming temp file: %w", err)
	}
	return nil
}

// Add appends a notification for user, trimming the user's feed to
// MaxPerUser (oldest dropped).
func (s *Store) Add(user, kind, message, ideaID, repoID string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id] = &Notification{
		ID: id, User: user, Kind: kind, Message: message,
		IdeaID: ideaID, RepoID: repoID, CreatedAt: time.Now().UTC(),
	}
	// Trim this user's overflow, oldest first.
	var mine []*Notification
	for _, n := range s.byID {
		if n.User == user {
			mine = append(mine, n)
		}
	}
	if len(mine) > MaxPerUser {
		sort.Slice(mine, func(i, j int) bool { return mine[i].CreatedAt.Before(mine[j].CreatedAt) })
		for _, n := range mine[:len(mine)-MaxPerUser] {
			delete(s.byID, n.ID)
		}
	}
	return s.persistLocked()
}

// ListByUser returns user's notifications, newest first. unreadOnly filters
// to unread.
func (s *Store) ListByUser(user string, unreadOnly bool) []Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Notification{}
	for _, n := range s.byID {
		if n.User != user || (unreadOnly && n.Read) {
			continue
		}
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// MarkRead marks the given notification IDs (or, with all=true, every one of
// user's notifications) read. IDs belonging to other users are ignored —
// users can only touch their own feed.
func (s *Store) MarkRead(user string, ids []string, all bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if all {
		for _, n := range s.byID {
			if n.User == user && !n.Read {
				n.Read = true
				changed = true
			}
		}
	}
	for _, id := range ids {
		if n, ok := s.byID[id]; ok && n.User == user && !n.Read {
			n.Read = true
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.persistLocked()
}
