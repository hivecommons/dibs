package store

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteJSONErrors(t *testing.T) {
	t.Run("marshal error", func(t *testing.T) {
		err := atomicWriteJSON(filepath.Join(t.TempDir(), "x.json"), math.NaN())
		if err == nil || !strings.Contains(err.Error(), "marshaling") {
			t.Fatalf("err = %v, want marshaling error", err)
		}
	})
	t.Run("temp file in missing dir", func(t *testing.T) {
		err := atomicWriteJSON(filepath.Join(t.TempDir(), "missing", "x.json"), 1)
		if err == nil || !strings.Contains(err.Error(), "creating temp file") {
			t.Fatalf("err = %v, want creating temp file error", err)
		}
	})
	t.Run("rename onto directory", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "x.json")
		if err := os.MkdirAll(filepath.Join(dest, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := atomicWriteJSON(dest, 1)
		if err == nil || !strings.Contains(err.Error(), "renaming temp file") {
			t.Fatalf("err = %v, want renaming temp file error", err)
		}
	})
}

func TestGetReadErrors(t *testing.T) {
	t.Run("indexed but file missing", func(t *testing.T) {
		dir := t.TempDir()
		s, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		idea := validIdea("alice")
		if err := s.Create(idea); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(s.ideaPath(idea.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(idea.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("corrupt idea file", func(t *testing.T) {
		dir := t.TempDir()
		s, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		idea := validIdea("alice")
		if err := s.Create(idea); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.ideaPath(idea.ID), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(idea.ID); err == nil || !strings.Contains(err.Error(), "corrupt idea") {
			t.Fatalf("err = %v, want corrupt idea error", err)
		}
	})
	t.Run("idea path unreadable", func(t *testing.T) {
		dir := t.TempDir()
		s, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		idea := validIdea("alice")
		if err := s.Create(idea); err != nil {
			t.Fatal(err)
		}
		// Replace the idea file with a directory: ReadFile fails with a
		// non-ErrNotExist error, exercising the wrapped-read branch.
		if err := os.Remove(s.ideaPath(idea.ID)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(s.ideaPath(idea.ID), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err = s.Get(idea.ID)
		if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "reading idea") {
			t.Fatalf("err = %v, want reading idea error", err)
		}
	})
}

func TestCreatePersistError(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Removing the data dir makes atomicWriteJSON's CreateTemp fail, so the
	// persistLocked error propagates out of Create.
	if err := os.RemoveAll(filepath.Join(dir, "ideas")); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(validIdea("alice")); err == nil || !strings.Contains(err.Error(), "creating temp file") {
		t.Fatalf("err = %v, want creating temp file error", err)
	}
}
