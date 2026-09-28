package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistLockedErrors(t *testing.T) {
	t.Run("temp file in missing dir", func(t *testing.T) {
		dir := t.TempDir()
		s, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		s.path = filepath.Join(dir, "missing", "notifications.json")
		if err := s.persistLocked(); err == nil || !strings.Contains(err.Error(), "creating temp file") {
			t.Fatalf("err = %v, want creating temp file error", err)
		}
	})
	t.Run("rename onto directory", func(t *testing.T) {
		dir := t.TempDir()
		s, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		s.path = filepath.Join(dir, "asdir")
		if err := os.MkdirAll(filepath.Join(s.path, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := s.persistLocked(); err == nil || !strings.Contains(err.Error(), "renaming temp file") {
			t.Fatalf("err = %v, want renaming temp file error", err)
		}
	})
}
