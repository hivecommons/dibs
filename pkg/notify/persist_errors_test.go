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
		// The failed rename must not replace the destination or leave the
		// temp file behind: /data is the only writable mount on the pod.
		if info, err := os.Stat(s.path); err != nil || !info.IsDir() {
			t.Fatalf("destination changed after failed persist: info = %v, err = %v", info, err)
		}
		assertNoTempFiles(t, dir)
	})
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temporary files after failed persist: %v", matches)
	}
}
