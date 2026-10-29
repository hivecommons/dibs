package news

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/registry"
)

type limitedFetcher struct {
	calls   atomic.Int32
	limited atomic.Bool
}

func (f *limitedFetcher) FetchMergedPullRequests(context.Context, string) ([]history.MergedPullRequest, error) {
	f.calls.Add(1)
	return nil, nil
}

func (f *limitedFetcher) RateLimited() bool { return f.limited.Load() }

func TestRefreshAsyncSkipsWhileFetcherRateLimited(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	f := &limitedFetcher{}
	f.limited.Store(true)
	g := NewGenerator(st, f, nil)
	g.Logf = t.Logf
	repos := []registry.RepoProfile{{RepoID: "org/repo"}}

	g.RefreshAsync(repos)
	time.Sleep(50 * time.Millisecond)
	if got := f.calls.Load(); got != 0 {
		t.Fatalf("fetches while limited = %d, want 0", got)
	}

	f.limited.Store(false)
	g.RefreshAsync(repos)
	// Wait for the refresh to persist, not merely to fetch: the goroutine still
	// writes under t.TempDir after the fetch, and returning early races its
	// cleanup ("TempDir RemoveAll cleanup: ... directory not empty").
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := st.fetchedAt("org/repo"); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if f.calls.Load() == 0 {
		t.Fatal("no fetch after limit lifted")
	}
	if _, ok := st.fetchedAt("org/repo"); !ok {
		t.Fatal("refresh never persisted after limit lifted")
	}
}
