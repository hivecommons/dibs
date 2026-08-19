// Package store persists Idea records as JSON files under DATA_DIR — one
// file per idea plus an index — with atomic temp+rename writes and a mutex
// guard. No external database, mirroring the hive file-store style.
package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Visibility values.
const (
	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

// Status values for an idea's lifecycle. Wave 1 only ever writes draft and
// offered; the rest are reserved for the matching/settlement waves so the
// schema doesn't churn.
const (
	StatusDraft       = "draft"
	StatusOffered     = "offered"
	StatusMatched     = "matched"
	StatusSubmitted   = "submitted"
	StatusAccepted    = "accepted"
	StatusRejected    = "rejected"
	StatusImplemented = "implemented"
)

// MaxBodyBytes caps an idea's markdown body.
const MaxBodyBytes = 64 * 1024

// MaxTitleLen caps an idea's title.
const MaxTitleLen = 200

// Match is a Wave-2 LLM match suggestion. Defined now so the on-disk schema
// is stable; always empty in Wave 1.
type Match struct {
	RepoID      string    `json:"repoID"`
	Score       float64   `json:"score"`
	Reason      string    `json:"reason"`
	SuggestedAt time.Time `json:"suggestedAt"`
}

// Idea is one ideator-authored idea.
type Idea struct {
	ID            string    `json:"id"`
	Author        string    `json:"author"`        // identity key (github login / provider:sub)
	AuthorDisplay string    `json:"authorDisplay"` // human name for credit
	Title         string    `json:"title"`
	Body          string    `json:"body"`           // markdown
	TLDR          string    `json:"tldr,omitempty"` // filled by the matching wave
	Visibility    string    `json:"visibility"`     // public | private
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Matches       []Match   `json:"matches"`
	TargetRepo    string    `json:"targetRepo,omitempty"`
	IssueURL      string    `json:"issueURL,omitempty"`
}

// ErrNotFound is returned when an idea does not exist.
var ErrNotFound = errors.New("store: idea not found")

// ValidationError describes a rejected write.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "store: " + e.Msg }

// Validate checks the user-writable fields.
func Validate(idea *Idea) error {
	if strings.TrimSpace(idea.Title) == "" {
		return &ValidationError{"title is required"}
	}
	if len(idea.Title) > MaxTitleLen {
		return &ValidationError{fmt.Sprintf("title exceeds %d characters", MaxTitleLen)}
	}
	if strings.TrimSpace(idea.Body) == "" {
		return &ValidationError{"body is required"}
	}
	if len(idea.Body) > MaxBodyBytes {
		return &ValidationError{fmt.Sprintf("body exceeds %d bytes", MaxBodyBytes)}
	}
	if idea.Visibility != VisibilityPublic && idea.Visibility != VisibilityPrivate {
		return &ValidationError{`visibility must be "public" or "private"`}
	}
	switch idea.Status {
	case StatusDraft, StatusOffered, StatusMatched, StatusSubmitted, StatusAccepted, StatusRejected, StatusImplemented:
	default:
		return &ValidationError{"invalid status"}
	}
	return nil
}

// indexEntry is the per-idea row kept in index.json so listings don't read
// every idea file.
type indexEntry struct {
	ID         string    `json:"id"`
	Author     string    `json:"author"`
	Visibility string    `json:"visibility"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Store is a mutex-guarded JSON file store.
type Store struct {
	mu    sync.RWMutex
	dir   string
	index map[string]indexEntry
}

// New opens (creating if needed) a store rooted at dir. Ideas live in
// dir/ideas/<id>.json; the index at dir/ideas/index.json.
func New(dir string) (*Store, error) {
	ideasDir := filepath.Join(dir, "ideas")
	if err := os.MkdirAll(ideasDir, 0o755); err != nil {
		return nil, fmt.Errorf("store: creating data dir: %w", err)
	}
	s := &Store{dir: ideasDir, index: map[string]indexEntry{}}
	raw, err := os.ReadFile(s.indexPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		// fresh store
	case err != nil:
		return nil, fmt.Errorf("store: reading index: %w", err)
	default:
		var entries []indexEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("store: corrupt index: %w", err)
		}
		for _, e := range entries {
			s.index[e.ID] = e
		}
	}
	return s, nil
}

func (s *Store) indexPath() string         { return filepath.Join(s.dir, "index.json") }
func (s *Store) ideaPath(id string) string { return filepath.Join(s.dir, id+".json") }

// newID returns a short random id (10 chars, url-safe).
func newID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("store: generating id: %w", err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

// atomicWriteJSON writes v to path via temp file + rename so a crash never
// leaves a torn file.
func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshaling: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("store: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("store: writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("store: closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("store: renaming temp file: %w", err)
	}
	return nil
}

// persistLocked writes the idea file and index. Caller holds s.mu.
func (s *Store) persistLocked(idea *Idea) error {
	if err := atomicWriteJSON(s.ideaPath(idea.ID), idea); err != nil {
		return err
	}
	s.index[idea.ID] = indexEntry{ID: idea.ID, Author: idea.Author, Visibility: idea.Visibility, UpdatedAt: idea.UpdatedAt}
	return s.writeIndexLocked()
}

func (s *Store) writeIndexLocked() error {
	entries := make([]indexEntry, 0, len(s.index))
	for _, e := range s.index {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return atomicWriteJSON(s.indexPath(), entries)
}

// Create validates and persists a new idea, filling ID/timestamps.
func (s *Store) Create(idea *Idea) error {
	if idea.Status == "" {
		idea.Status = StatusDraft
	}
	if err := Validate(idea); err != nil {
		return err
	}
	id, err := newID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	idea.ID = id
	idea.CreatedAt = now
	idea.UpdatedAt = now
	if idea.Matches == nil {
		idea.Matches = []Match{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistLocked(idea)
}

// Get returns a copy of the idea.
func (s *Store) Get(id string) (*Idea, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readLocked(id)
}

func (s *Store) readLocked(id string) (*Idea, error) {
	if _, ok := s.index[id]; !ok {
		return nil, ErrNotFound
	}
	raw, err := os.ReadFile(s.ideaPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: reading idea: %w", err)
	}
	var idea Idea
	if err := json.Unmarshal(raw, &idea); err != nil {
		return nil, fmt.Errorf("store: corrupt idea %s: %w", id, err)
	}
	return &idea, nil
}

// Update validates and persists an existing idea, bumping UpdatedAt. The
// caller is responsible for authorization; ID/Author/CreatedAt are preserved
// from the stored record.
func (s *Store) Update(idea *Idea) error {
	if err := Validate(idea); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.readLocked(idea.ID)
	if err != nil {
		return err
	}
	idea.Author = existing.Author
	idea.CreatedAt = existing.CreatedAt
	idea.UpdatedAt = time.Now().UTC()
	if idea.Matches == nil {
		idea.Matches = existing.Matches
	}
	return s.persistLocked(idea)
}

// Delete removes an idea. The caller is responsible for authorization.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.index[id]; !ok {
		return ErrNotFound
	}
	if err := os.Remove(s.ideaPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: deleting idea: %w", err)
	}
	delete(s.index, id)
	return s.writeIndexLocked()
}

// ListByAuthor returns every idea (any visibility) owned by author, newest
// first.
func (s *Store) ListByAuthor(author string) ([]*Idea, error) {
	return s.list(func(e indexEntry) bool { return e.Author == author })
}

// ListPublic returns every PUBLIC idea, newest first. Private ideas are
// filtered at the index level so they can never leak into a listing —
// this is the invariant the private-idea tests pin down.
func (s *Store) ListPublic() ([]*Idea, error) {
	return s.list(func(e indexEntry) bool { return e.Visibility == VisibilityPublic })
}

func (s *Store) list(keep func(indexEntry) bool) ([]*Idea, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Idea
	for _, e := range s.index {
		if !keep(e) {
			continue
		}
		idea, err := s.readLocked(e.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, idea)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if out == nil {
		out = []*Idea{}
	}
	return out, nil
}
