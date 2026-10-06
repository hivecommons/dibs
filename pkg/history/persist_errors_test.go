package history

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpsertPersistError(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(t.TempDir(), "missing", "repo-history.json")
	err = s.Upsert("org/repo", nil, time.Now())
	if err == nil || !strings.HasPrefix(err.Error(), "history: creating temp file:") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want wrapped history persistence error", err)
	}
}
