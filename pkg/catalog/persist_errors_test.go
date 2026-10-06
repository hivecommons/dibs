package catalog

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshPersistError(t *testing.T) {
	s, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	s.Client = &http.Client{Transport: &landscapeTransport{landscape: func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("landscape: []"))
	}}}
	s.path = filepath.Join(t.TempDir(), "missing", CacheFile)
	err = s.Refresh(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "catalog: creating temp file:") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want wrapped catalog persistence error", err)
	}
}
