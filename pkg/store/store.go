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

// Status values for an idea's lifecycle state machine:
//
//	draft ──offer──▶ offered ──accept──▶ accepted ──issue──▶ settled
//	  │                 │
//	  │                 └──decline──▶ declined ──re-offer──▶ offered
//	  └──direct accept (public ideas only)──▶ accepted
const (
	StatusDraft    = "draft"
	StatusOffered  = "offered"
	StatusAccepted = "accepted"
	StatusDeclined = "declined"
	StatusSettled  = "settled"
)

// CanTransition reports whether the idea state machine allows from→to.
func CanTransition(from, to string) bool {
	switch from {
	case StatusDraft:
		return to == StatusOffered || to == StatusAccepted
	case StatusOffered:
		return to == StatusAccepted || to == StatusDeclined
	case StatusDeclined:
		return to == StatusOffered
	case StatusAccepted:
		return to == StatusSettled
	default:
		return false
	}
}

// Offer status values (per-repo offers hanging off an idea).
const (
	OfferPending  = "pending"
	OfferAccepted = "accepted"
	OfferDeclined = "declined"
)

// Offer records the ideator explicitly offering the idea to one repo. For a
// PRIVATE idea, an offer is the one and only thing that reveals it to that
// repo's owner.
type Offer struct {
	RepoID    string     `json:"repoID"`
	Status    string     `json:"status"` // pending | accepted | declined
	CreatedAt time.Time  `json:"createdAt"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
}

// MaxBodyBytes caps an idea's markdown body.
const MaxBodyBytes = 64 * 1024

// MaxTitleLen caps an idea's title.
const MaxTitleLen = 200

// Match is a cached idea↔repo fit score. RepoHash fingerprints the repo
// profile the score was computed against so repo edits invalidate the cache;
// idea edits clear Matches wholesale (see Update).
type Match struct {
	RepoID      string    `json:"repoID"`
	Score       float64   `json:"score"`
	Reason      string    `json:"reason"`
	SuggestedAt time.Time `json:"suggestedAt"`
	RepoHash    string    `json:"repoHash,omitempty"`
	// ByLLM records whether the score came from the LLM (vs the
	// deterministic fallback).
	ByLLM bool `json:"byLLM,omitempty"`
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
	// Offers are the repos the ideator explicitly offered this idea to.
	Offers []Offer `json:"offers,omitempty"`
	// PassedRepos are repos the ideator swiped away; they never resurface
	// in the idea's match candidates.
	PassedRepos []string `json:"passedRepos,omitempty"`
	TargetRepo  string   `json:"targetRepo,omitempty"`
	IssueURL    string   `json:"issueURL,omitempty"`
}

// OfferTo returns the idea's offer to repoID, or nil.
func (i *Idea) OfferTo(repoID string) *Offer {
	for k := range i.Offers {
		if i.Offers[k].RepoID == repoID {
			return &i.Offers[k]
		}
	}
	return nil
}

// HasPassed reports whether the ideator swiped repoID away.
func (i *Idea) HasPassed(repoID string) bool {
	for _, r := range i.PassedRepos {
		if r == repoID {
			return true
		}
	}
	return false
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
	case StatusDraft, StatusOffered, StatusAccepted, StatusDeclined, StatusSettled:
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
// caller is responsible for authorization; ID/Author/CreatedAt and the
// server-managed fields (offers, passes, target, issue URL) are preserved
// from the stored record. Editing the CONTENT (title/body) invalidates the
// cached TLDR and matches so the match engine recomputes them.
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
	idea.Offers = existing.Offers
	idea.PassedRepos = existing.PassedRepos
	idea.TargetRepo = existing.TargetRepo
	idea.IssueURL = existing.IssueURL
	if idea.Title != existing.Title || idea.Body != existing.Body {
		idea.TLDR = ""
		idea.Matches = []Match{}
	} else {
		idea.TLDR = existing.TLDR
		if idea.Matches == nil {
			idea.Matches = existing.Matches
		}
	}
	return s.persistLocked(idea)
}

// Mutate atomically read-modify-writes an idea under the store lock. fn may
// change any field except identity/timestamps; the result is re-validated.
// touch controls whether UpdatedAt is bumped (cache refreshes shouldn't
// reorder listings). Returns a copy of the persisted idea.
func (s *Store) Mutate(id string, touch bool, fn func(*Idea) error) (*Idea, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idea, err := s.readLocked(id)
	if err != nil {
		return nil, err
	}
	if err := fn(idea); err != nil {
		return nil, err
	}
	if err := Validate(idea); err != nil {
		return nil, err
	}
	if touch {
		idea.UpdatedAt = time.Now().UTC()
	}
	if err := s.persistLocked(idea); err != nil {
		return nil, err
	}
	cp := *idea
	return &cp, nil
}

// Transition moves an idea through the state machine, rejecting any move
// CanTransition disallows.
func (s *Store) Transition(id, to string) (*Idea, error) {
	return s.Mutate(id, true, func(idea *Idea) error {
		if !CanTransition(idea.Status, to) {
			return &ValidationError{fmt.Sprintf("cannot transition %s → %s", idea.Status, to)}
		}
		idea.Status = to
		return nil
	})
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

// ListOfferedTo returns every idea — INCLUDING private ones — that carries a
// PENDING offer to one of repoIDs, newest first. This is the only path by
// which a private idea reaches anyone but its author: the ideator's explicit
// offer to that specific repo.
func (s *Store) ListOfferedTo(repoIDs []string) ([]*Idea, error) {
	want := map[string]bool{}
	for _, r := range repoIDs {
		want[r] = true
	}
	all, err := s.list(func(indexEntry) bool { return true })
	if err != nil {
		return nil, err
	}
	out := []*Idea{}
	for _, idea := range all {
		for _, o := range idea.Offers {
			if o.Status == OfferPending && want[o.RepoID] {
				out = append(out, idea)
				break
			}
		}
	}
	return out, nil
}

// ListSettled returns every SETTLED idea (any visibility), newest first.
// Settled ideas power the public credit wall: settlement opened a public
// GitHub issue, so the idea's existence, title, TLDR, author, and issue URL
// are already public — the credit wall exposes exactly that and nothing
// more (never the body, never unsettled ideas).
func (s *Store) ListSettled() ([]*Idea, error) {
	all, err := s.list(func(indexEntry) bool { return true })
	if err != nil {
		return nil, err
	}
	out := []*Idea{}
	for _, idea := range all {
		if idea.Status == StatusSettled {
			out = append(out, idea)
		}
	}
	return out, nil
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
