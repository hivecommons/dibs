package fsutil

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temporary files: %v", matches)
	}
}

func TestAtomicWriteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	// Both initial creation and replacement must use the same JSON formatting
	// and private file mode, without leaving temporary files behind.
	for _, value := range []map[string]int{{"count": 1}, {"count": 2}} {
		if err := AtomicWriteJSON(path, value); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("JSON = %q, want %q", got, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o, want 600", info.Mode().Perm())
		}
		assertNoTempFiles(t, dir)
	}
}

func TestAtomicWriteJSONErrors(t *testing.T) {
	t.Run("marshal error preserves existing file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "x.json")
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := AtomicWriteJSON(path, math.NaN())
		var unsupported *json.UnsupportedValueError
		if err == nil || !strings.HasPrefix(err.Error(), "marshaling:") || !errors.As(err, &unsupported) {
			t.Fatalf("err = %v, want wrapped marshaling error", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "original" {
			t.Fatalf("original file = %q, err = %v", got, err)
		}
		assertNoTempFiles(t, dir)
	})
	t.Run("temp file in missing dir", func(t *testing.T) {
		err := AtomicWriteJSON(filepath.Join(t.TempDir(), "missing", "x.json"), 1)
		if err == nil || !strings.HasPrefix(err.Error(), "creating temp file:") || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("err = %v, want wrapped creating temp file error", err)
		}
	})
	t.Run("rename onto directory cleans up", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "x.json")
		if err := os.MkdirAll(filepath.Join(dest, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := AtomicWriteJSON(dest, 1)
		var linkErr *os.LinkError
		if err == nil || !strings.HasPrefix(err.Error(), "renaming temp file:") || !errors.As(err, &linkErr) {
			t.Fatalf("err = %v, want wrapped renaming temp file error", err)
		}
		if info, err := os.Stat(dest); err != nil || !info.IsDir() {
			t.Fatalf("destination directory changed: info = %v, err = %v", info, err)
		}
		assertNoTempFiles(t, dir)
	})
}
