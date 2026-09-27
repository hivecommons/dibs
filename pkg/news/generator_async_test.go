package news

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/registry"
)

// blockingFetcher parks FetchMergedPullRequests until release is closed so
// tests can observe the in-flight dedup window.
type blockingFetcher struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	prs     []history.MergedPullRequest
}

func (f *blockingFetcher) FetchMergedPullRequests(ctx context.Context, repoID string) ([]history.MergedPullRequest, error) {
	f.mu.Lock()
	f.calls++
	if f.calls == 1 {
		close(f.started)
	}
	f.mu.Unlock()
	<-f.release
	return append([]history.MergedPullRequest(nil), f.prs...), nil
}

func (f *blockingFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestNewGeneratorWiring(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	f := &fakeFetcher{}
	g := NewGenerator(st, f, nil)
	if g.Store != st || g.Fetcher == nil || g.Logf == nil {
		t.Fatalf("NewGenerator wiring incomplete: %+v", g)
	}
}

func TestRefreshAsyncGuardsAndDedup(t *testing.T) {
	// Nil / unwired generators must be safe no-ops.
	var nilGen *Generator
	nilGen.RefreshAsync([]registry.RepoProfile{{RepoID: "org/repo"}})
	(&Generator{}).RefreshAsync([]registry.RepoProfile{{RepoID: "org/repo"}})

	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	f := &blockingFetcher{
		started: make(chan struct{}),
		release: make(chan struct{}),
		prs:     []history.MergedPullRequest{pr("merged work", "2026-08-19T09:00:00Z", "alice")},
	}
	g := NewGenerator(st, f, nil)
	g.Now = newsNow
	g.Logf = nil

	repos := []registry.RepoProfile{{RepoID: "org/repo"}}
	g.RefreshAsync(repos)
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh goroutine never started")
	}

	// While org/repo is active, further RefreshAsync calls must be dropped.
	g.RefreshAsync(repos)
	if got := f.callCount(); got != 1 {
		t.Fatalf("fetch calls during active refresh = %d, want 1", got)
	}

	close(f.release)
	deadline := time.Now().Add(5 * time.Second)
	for len(st.Get("org/repo")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("refresh never persisted news items")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTruncateOneLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "short passes through", in: "hello world", max: 20, want: "hello world"},
		{name: "whitespace collapses", in: "  a\n\tb   c  ", max: 20, want: "a b c"},
		{name: "truncates with ellipsis", in: "abcdefgh", max: 5, want: "abcd…"},
		{name: "trims before ellipsis", in: "abc defgh", max: 5, want: "abc…"},
		{name: "max one keeps one rune", in: "abc", max: 1, want: "a"},
		{name: "multibyte runes counted", in: "héllo wörld", max: 6, want: "héllo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateOneLine(tt.in, tt.max); got != tt.want {
				t.Fatalf("truncateOneLine(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}
