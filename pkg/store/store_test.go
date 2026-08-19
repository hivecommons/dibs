package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, dir
}

func validIdea(author string) *Idea {
	return &Idea{
		Author:        author,
		AuthorDisplay: author + " Display",
		Title:         "Test idea",
		Body:          "A body in **markdown**.",
		Visibility:    VisibilityPublic,
	}
}

func TestCreateGetUpdateDelete(t *testing.T) {
	s, _ := newTestStore(t)

	idea := validIdea("alice")
	if err := s.Create(idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if idea.ID == "" || idea.Status != StatusDraft || idea.CreatedAt.IsZero() {
		t.Fatalf("Create did not fill defaults: %+v", idea)
	}
	if idea.Matches == nil || len(idea.Matches) != 0 {
		t.Fatalf("Matches should be empty non-nil, got %#v", idea.Matches)
	}

	got, err := s.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != idea.Title || got.Author != "alice" {
		t.Fatalf("Get mismatch: %+v", got)
	}

	got.Title = "Updated title"
	got.Author = "mallory" // must NOT be persistable via Update
	got.Status = StatusOffered
	if err := s.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, _ := s.Get(idea.ID)
	if got2.Title != "Updated title" || got2.Status != StatusOffered {
		t.Fatalf("Update lost fields: %+v", got2)
	}
	if got2.Author != "alice" {
		t.Fatalf("Update let author change: %q", got2.Author)
	}
	if !got2.UpdatedAt.After(got2.CreatedAt) {
		t.Fatalf("UpdatedAt not bumped: %+v", got2)
	}

	if err := s.Delete(idea.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(idea.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: want ErrNotFound, got %v", err)
	}
	if err := s.Delete(idea.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double Delete: want ErrNotFound, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	s, _ := newTestStore(t)
	cases := []struct {
		name   string
		mutate func(*Idea)
	}{
		{"empty title", func(i *Idea) { i.Title = "  " }},
		{"empty body", func(i *Idea) { i.Body = "" }},
		{"long title", func(i *Idea) { i.Title = strings.Repeat("x", MaxTitleLen+1) }},
		{"oversized body", func(i *Idea) { i.Body = strings.Repeat("x", MaxBodyBytes+1) }},
		{"bad visibility", func(i *Idea) { i.Visibility = "friends-only" }},
		{"bad status", func(i *Idea) { i.Status = "bogus" }},
	}
	for _, tc := range cases {
		idea := validIdea("alice")
		tc.mutate(idea)
		err := s.Create(idea)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: want ValidationError, got %v", tc.name, err)
		}
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	s, dir := newTestStore(t)
	idea := validIdea("alice")
	if err := s.Create(idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := s2.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.Title != idea.Title {
		t.Fatalf("reopen lost data: %+v", got)
	}
}

// TestAtomicWriteLeavesNoTempFiles verifies temp+rename hygiene: after many
// writes, the ideas dir contains only <id>.json files and the index.
func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	s, dir := newTestStore(t)
	for range 20 {
		if err := s.Create(validIdea("alice")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "ideas"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("unexpected file: %s", e.Name())
		}
	}
	// Every file must be complete, parseable JSON (no torn writes).
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, "ideas", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if !json.Valid(raw) {
			t.Fatalf("torn/invalid JSON in %s", e.Name())
		}
	}
}

// TestConcurrentWrites exercises the mutex guard under -race.
func TestConcurrentWrites(t *testing.T) {
	s, _ := newTestStore(t)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			idea := validIdea("alice")
			if err := s.Create(idea); err != nil {
				t.Errorf("Create %d: %v", n, err)
				return
			}
			idea.Title = "updated"
			if err := s.Update(idea); err != nil {
				t.Errorf("Update %d: %v", n, err)
			}
			if _, err := s.ListByAuthor("alice"); err != nil {
				t.Errorf("List %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()
	ideas, err := s.ListByAuthor("alice")
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(ideas) != 10 {
		t.Fatalf("want 10 ideas, got %d", len(ideas))
	}
}

// TestPrivateInvariantAtStoreLevel: ListPublic must NEVER return a private
// idea, regardless of status.
func TestPrivateInvariantAtStoreLevel(t *testing.T) {
	s, _ := newTestStore(t)

	priv := validIdea("alice")
	priv.Visibility = VisibilityPrivate
	priv.Status = StatusOffered
	if err := s.Create(priv); err != nil {
		t.Fatalf("Create private: %v", err)
	}
	pub := validIdea("alice")
	if err := s.Create(pub); err != nil {
		t.Fatalf("Create public: %v", err)
	}

	public, err := s.ListPublic()
	if err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	for _, i := range public {
		if i.Visibility == VisibilityPrivate || i.ID == priv.ID {
			t.Fatalf("PRIVATE IDEA LEAKED into public listing: %+v", i)
		}
	}
	if len(public) != 1 || public[0].ID != pub.ID {
		t.Fatalf("public listing wrong: %+v", public)
	}

	// The author still sees both.
	mine, err := s.ListByAuthor("alice")
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(mine) != 2 {
		t.Fatalf("author should see 2 ideas, got %d", len(mine))
	}
}
