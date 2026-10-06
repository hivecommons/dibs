package news

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
	s.path = filepath.Join(t.TempDir(), "missing", "repo-news.json")
	err = s.upsert("org/repo", nil, time.Now())
	if err == nil || !strings.HasPrefix(err.Error(), "news: creating temp file:") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want wrapped news persistence error", err)
	}
}
