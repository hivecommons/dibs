package history

import (
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
		dest := filepath.Join(t.TempDir(), "x.json")
		if err := os.MkdirAll(filepath.Join(dest, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := atomicWriteJSON(dest, 1)
		if err == nil || !strings.Contains(err.Error(), "renaming temp file") {
			t.Fatalf("err = %v, want renaming temp file error", err)
		}
	})
}
