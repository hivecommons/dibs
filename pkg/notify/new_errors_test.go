package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewErrors: New refuses an unusable data dir, an unreadable
// notifications.json, and a corrupt one, with distinct error messages.
func TestNewErrors(t *testing.T) {
	t.Run("data dir is a file", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "notadir")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := New(file)
		if s != nil || err == nil || !strings.Contains(err.Error(), "notify: creating data dir") {
			t.Fatalf("New(file) = %v, %v; want creating data dir error", s, err)
		}
	})
	t.Run("notifications.json is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "notifications.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		s, err := New(dir)
		if s != nil || err == nil || !strings.Contains(err.Error(), "notify: reading notifications.json") {
			t.Fatalf("New = %v, %v; want reading notifications.json error", s, err)
		}
	})
	t.Run("corrupt notifications.json", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "notifications.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := New(dir)
		if s != nil || err == nil || !strings.Contains(err.Error(), "notify: corrupt notifications.json") {
			t.Fatalf("New = %v, %v; want corrupt notifications.json error", s, err)
		}
	})
}

// TestNewReloadsPersistedFeed: a store reopened on the same dir sees the
// notifications the previous instance persisted, proving New's default arm
// round-trips what persistLocked wrote.
func TestNewReloadsPersistedFeed(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustAdd(t, s, "alice", "match", "hello")
	mustAdd(t, s, "bob", "match", "world")

	reopened, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.ListByUser("alice", false); len(got) != 1 || got[0].Message != "hello" {
		t.Fatalf("alice after reopen = %+v, want one 'hello' notification", got)
	}
	if got := reopened.ListByUser("bob", false); len(got) != 1 || got[0].Message != "world" {
		t.Fatalf("bob after reopen = %+v, want one 'world' notification", got)
	}
}

// TestMutationsSurfacePersistErrors: Add and MarkRead return (rather than
// swallow) a persist failure once the store's path becomes unwritable.
func TestMutationsSurfacePersistErrors(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustAdd(t, s, "alice", "match", "hello")
	id := s.ListByUser("alice", false)[0].ID

	s.path = filepath.Join(dir, "missing", "notifications.json")
	if err := s.Add("alice", "match", "again", "idea1", "org/repo"); err == nil || !strings.Contains(err.Error(), "creating temp file") {
		t.Fatalf("Add err = %v, want creating temp file error", err)
	}
	if err := s.MarkRead("alice", []string{id}, false); err == nil || !strings.Contains(err.Error(), "creating temp file") {
		t.Fatalf("MarkRead err = %v, want creating temp file error", err)
	}
}
