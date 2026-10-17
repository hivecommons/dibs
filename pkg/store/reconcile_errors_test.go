package store

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hivecommons/dibs/pkg/fsutil"
)

// An idea file that exists in the directory listing but cannot be read must
// be skipped, not adopted, and must not abort reconciliation of its siblings.
func TestNewReconcileSkipsUnreadableIdeaFile(t *testing.T) {
	s, dir := newTestStore(t)
	orphan := validIdea("bob")
	orphan.ID = "orphan0001"
	orphan.Matches = []Match{}
	if err := fsutil.AtomicWriteJSON(s.ideaPath(orphan.ID), orphan); err != nil {
		t.Fatal(err)
	}
	// A dangling symlink lists as a regular (non-dir) entry but fails ReadFile.
	if err := os.Symlink(s.ideaPath("doesnotexist"), s.ideaPath("dangling01")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s2.Get("dangling01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dangling err = %v, want ErrNotFound", err)
	}
	if _, err := s2.Get(orphan.ID); err != nil {
		t.Fatalf("Get orphan after skipping unreadable sibling: %v", err)
	}
	raw, err := os.ReadFile(s2.indexPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dangling01") {
		t.Fatalf("index adopted unreadable file: %s", raw)
	}
}

// A data directory whose ideas/ cannot be listed must fail New with the
// wrapped scan error rather than silently opening an empty store.
func TestNewReconcileScanError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	s, dir := newTestStore(t)
	if err := s.Create(validIdea("alice")); err != nil {
		t.Fatal(err)
	}
	// Search-only mode: index.json stays readable by path, but ReadDir fails.
	if err := os.Chmod(s.dir, 0o300); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.dir, 0o755) })

	s2, err := New(dir)
	if err == nil || !strings.HasPrefix(err.Error(), "store: scanning ideas dir:") || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("New err = %v, want wrapped scanning ideas dir permission error", err)
	}
	if s2 != nil {
		t.Fatal("New returned a store alongside an error")
	}
}
