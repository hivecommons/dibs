package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// breakIndexFile replaces ideas/index.json with a non-empty directory so the
// atomic rename inside writeIndexLocked fails. Reads are unaffected because
// the index is held in memory, which isolates the persist error path.
func breakIndexFile(t *testing.T, s *Store) {
	t.Helper()
	if err := os.Remove(s.indexPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.indexPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.indexPath(), "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateErrors(t *testing.T) {
	t.Run("validation failure is returned before any read", func(t *testing.T) {
		s, _ := newTestStore(t)
		idea := validIdea("alice")
		if err := s.Create(idea); err != nil {
			t.Fatal(err)
		}
		edit := *idea
		edit.Title = "   "
		var verr *ValidationError
		if err := s.Update(&edit); !errors.As(err, &verr) {
			t.Fatalf("err = %v, want *ValidationError", err)
		}
		got, err := s.Get(idea.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Title != idea.Title {
			t.Fatalf("stored title = %q, want unchanged %q", got.Title, idea.Title)
		}
	})
	t.Run("unknown id is ErrNotFound", func(t *testing.T) {
		s, _ := newTestStore(t)
		idea := validIdea("alice")
		idea.ID = "doesnotexist"
		if err := s.Update(idea); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("legacy record without authorProvider is backfilled", func(t *testing.T) {
		s, _ := newTestStore(t)
		idea := validIdea("gitlab:bob")
		if err := s.Create(idea); err != nil {
			t.Fatal(err)
		}
		if idea.AuthorProvider != "gitlab" {
			t.Fatalf("Create set AuthorProvider = %q, want gitlab", idea.AuthorProvider)
		}
		// Simulate a record written before AuthorProvider existed by
		// stripping the field from the on-disk JSON.
		raw, err := os.ReadFile(s.ideaPath(idea.ID))
		if err != nil {
			t.Fatal(err)
		}
		stripped := strings.Replace(string(raw), `"authorProvider": "gitlab",`, "", 1)
		if stripped == string(raw) {
			t.Fatalf("fixture did not contain authorProvider field:\n%s", raw)
		}
		if err := os.WriteFile(s.ideaPath(idea.ID), []byte(stripped), 0o644); err != nil {
			t.Fatal(err)
		}
		before, err := s.Get(idea.ID)
		if err != nil {
			t.Fatal(err)
		}
		if before.AuthorProvider != "" {
			t.Fatalf("fixture AuthorProvider = %q, want empty", before.AuthorProvider)
		}

		edit := *before
		edit.AuthorProvider = "attacker-supplied"
		if err := s.Update(&edit); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if edit.AuthorProvider != "gitlab" {
			t.Fatalf("AuthorProvider after Update = %q, want gitlab derived from author", edit.AuthorProvider)
		}
		after, err := s.Get(idea.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.AuthorProvider != "gitlab" {
			t.Fatalf("persisted AuthorProvider = %q, want gitlab", after.AuthorProvider)
		}
	})
}

func TestMutatePersistError(t *testing.T) {
	s, _ := newTestStore(t)
	idea := validIdea("alice")
	if err := s.Create(idea); err != nil {
		t.Fatal(err)
	}
	breakIndexFile(t, s)
	_, err := s.Mutate(idea.ID, true, func(i *Idea) error {
		i.Body = "changed"
		return nil
	})
	if err == nil || !strings.HasPrefix(err.Error(), "store: renaming temp file:") {
		t.Fatalf("err = %v, want renaming temp file error", err)
	}
}

func TestDeleteRemoveError(t *testing.T) {
	s, _ := newTestStore(t)
	idea := validIdea("alice")
	if err := s.Create(idea); err != nil {
		t.Fatal(err)
	}
	// Replace the idea file with a non-empty directory: os.Remove fails with
	// something other than ErrNotExist, so Delete must surface it and keep
	// the index entry rather than silently forgetting the idea.
	if err := os.Remove(s.ideaPath(idea.ID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.ideaPath(idea.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.ideaPath(idea.ID), "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := s.Delete(idea.ID)
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "deleting idea") {
		t.Fatalf("err = %v, want deleting idea error", err)
	}
	s.mu.RLock()
	_, stillIndexed := s.index[idea.ID]
	s.mu.RUnlock()
	if !stillIndexed {
		t.Fatal("Delete dropped the index entry despite failing to remove the file")
	}
}

func TestListFilteredReadErrors(t *testing.T) {
	s, _ := newTestStore(t)
	idea := validIdea("alice")
	if err := s.Create(idea); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.ideaPath(idea.ID), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListOfferedTo([]string{"r1"}); err == nil || !strings.Contains(err.Error(), "corrupt idea") {
		t.Fatalf("ListOfferedTo err = %v, want corrupt idea error", err)
	}
	if _, err := s.ListSettled(); err == nil || !strings.Contains(err.Error(), "corrupt idea") {
		t.Fatalf("ListSettled err = %v, want corrupt idea error", err)
	}
}
