package fsutil

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteJSON(t *testing.T) {
	t.Run("writes file and leaves no temp files", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "x.json")
		if err := AtomicWriteJSON(dest, map[string]int{"a": 1}, ".tmp-*"); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		if want := "{\n  \"a\": 1\n}"; string(got) != want {
			t.Fatalf("content = %q, want %q", got, want)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("dir has %d entries, want 1", len(entries))
		}
	})
}

func TestAtomicWriteJSONErrors(t *testing.T) {
	t.Run("marshal error", func(t *testing.T) {
		err := AtomicWriteJSON(filepath.Join(t.TempDir(), "x.json"), math.NaN(), ".tmp-*")
		if err == nil || !strings.Contains(err.Error(), "marshaling") {
			t.Fatalf("err = %v, want marshaling error", err)
		}
	})
	t.Run("temp file in missing dir", func(t *testing.T) {
		err := AtomicWriteJSON(filepath.Join(t.TempDir(), "missing", "x.json"), 1, ".tmp-*")
		if err == nil || !strings.Contains(err.Error(), "creating temp file") {
			t.Fatalf("err = %v, want creating temp file error", err)
		}
	})
	t.Run("rename onto directory", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "x.json")
		if err := os.MkdirAll(filepath.Join(dest, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := AtomicWriteJSON(dest, 1, ".tmp-*")
		if err == nil || !strings.Contains(err.Error(), "renaming temp file") {
			t.Fatalf("err = %v, want renaming temp file error", err)
		}
	})
}
