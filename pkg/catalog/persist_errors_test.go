package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshPersistError(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer github.Close()
	s := newRefreshStore(t, github, serveLandscape)
	s.path = filepath.Join(t.TempDir(), "missing", CacheFile)
	err := s.Refresh(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "catalog: creating temp file:") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want wrapped catalog persistence error", err)
	}
	if n := len(s.List()); n != 0 {
		t.Fatalf("len(List()) = %d after failed persist, want 0", n)
	}
	if !s.needsRefresh() {
		t.Fatal("needsRefresh() = false after failed persist, want true")
	}
}
