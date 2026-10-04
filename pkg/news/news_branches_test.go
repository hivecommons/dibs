package news

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/registry"
)

type errFetcher struct{ err error }

func (f errFetcher) FetchMergedPullRequests(context.Context, string) ([]history.MergedPullRequest, error) {
	return nil, f.err
}

func TestStoreGetGuards(t *testing.T) {
	var nilStore *Store
	if got := nilStore.Get("org/repo"); got != nil {
		t.Fatalf("nil store Get = %v, want nil", got)
	}

	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := st.Get("org/unknown"); got != nil {
		t.Fatalf("unknown repo Get = %v, want nil", got)
	}

	// Get must cap at MaxItems even if the in-memory record carries more
	// (e.g. a hand-edited file loaded before normalization rules tightened).
	over := make([]cachedItem, 0, MaxItems+3)
	for i := 0; i < MaxItems+3; i++ {
		over = append(over, cachedItem{Item: Item{Date: fmt.Sprintf("2026-08-%02d", i+1), TLDR: "x."}})
	}
	st.mu.Lock()
	st.data["org/over"] = repoNews{RepoID: "org/over", Items: over}
	st.mu.Unlock()
	if got := st.Get("org/over"); len(got) != MaxItems {
		t.Fatalf("Get returned %d items, want %d", len(got), MaxItems)
	}
}

func TestNormalizeRepoNewsTrimsToMaxItemsNewestFirst(t *testing.T) {
	items := make([]cachedItem, 0, MaxItems+6)
	for i := 0; i < MaxItems+6; i++ {
		items = append(items, cachedItem{Item: Item{Date: fmt.Sprintf("2026-07-%02d", i+1), TLDR: "x."}})
	}
	rn := normalizeRepoNews(repoNews{RepoID: "org/repo", Items: items})
	if len(rn.Items) != MaxItems {
		t.Fatalf("normalized to %d items, want %d", len(rn.Items), MaxItems)
	}
	if rn.Items[0].Date != "2026-07-20" || rn.Items[MaxItems-1].Date != "2026-07-07" {
		t.Fatalf("items not newest-first after trim: first=%s last=%s", rn.Items[0].Date, rn.Items[MaxItems-1].Date)
	}
}

func TestPersistSortsReposByID(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := newsNow()
	for _, id := range []string{"zeta/repo", "alpha/repo", "mid/repo"} {
		if err := st.upsert(id, []cachedItem{{Item: Item{Date: "2026-08-19", TLDR: "x."}}}, now); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "repo-news.json"))
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	var persisted []repoNews
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(persisted) != 3 || persisted[0].RepoID != "alpha/repo" || persisted[1].RepoID != "mid/repo" || persisted[2].RepoID != "zeta/repo" {
		ids := make([]string, 0, len(persisted))
		for _, rn := range persisted {
			ids = append(ids, rn.RepoID)
		}
		t.Fatalf("persisted order = %v, want sorted by RepoID", ids)
	}
}

func TestRefreshGuardsAndErrors(t *testing.T) {
	var nilGen *Generator
	if err := nilGen.Refresh(context.Background(), "org/repo"); err != nil {
		t.Fatalf("nil generator Refresh = %v, want nil", err)
	}
	if err := (&Generator{}).Refresh(context.Background(), "org/repo"); err != nil {
		t.Fatalf("unwired generator Refresh = %v, want nil", err)
	}

	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	t.Run("fetcher error propagates", func(t *testing.T) {
		boom := errors.New("hub down")
		g := &Generator{Store: st, Fetcher: errFetcher{err: boom}, Now: newsNow}
		if err := g.Refresh(context.Background(), "org/repo"); !errors.Is(err, boom) {
			t.Fatalf("Refresh err = %v, want %v", err, boom)
		}
		if got := st.Get("org/repo"); got != nil {
			t.Fatalf("failed refresh must not persist items, got %v", got)
		}
	})

	t.Run("cancelled context while semaphore is full", func(t *testing.T) {
		g := &Generator{Store: st, Fetcher: &fakeFetcher{}, Now: newsNow}
		sem := g.semaphore()
		for i := 0; i < cap(sem); i++ {
			sem <- struct{}{}
		}
		defer func() {
			for i := 0; i < cap(sem); i++ {
				<-sem
			}
		}()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := g.Refresh(ctx, "org/repo"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Refresh err = %v, want context.Canceled", err)
		}
	})
}

func TestRefreshAsyncLogsFetchError(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	logged := make(chan string, 1)
	g := &Generator{
		Store:   st,
		Fetcher: errFetcher{err: errors.New("hub down")},
		Now:     newsNow,
		Logf: func(format string, args ...any) {
			select {
			case logged <- fmt.Sprintf(format, args...):
			default:
			}
		},
	}
	g.RefreshAsync([]registry.RepoProfile{{RepoID: "org/repo"}})
	select {
	case msg := <-logged:
		if !strings.Contains(msg, "news refresh org/repo") || !strings.Contains(msg, "hub down") {
			t.Fatalf("log message = %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshAsync never logged the fetch error")
	}
}

func TestGeneratorNowDefaultsToUTC(t *testing.T) {
	before := time.Now().UTC()
	got := (&Generator{}).now()
	after := time.Now().UTC()
	if got.Location() != time.UTC {
		t.Fatalf("now() location = %v, want UTC", got.Location())
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("now() = %v outside [%v, %v]", got, before, after)
	}
}

func TestDigestFallsBackAndLogsOnLLMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer srv.Close()

	var logged []string
	g := &Generator{
		LLM:  &match.LLM{BaseURL: srv.URL, Model: "test", Client: srv.Client()},
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	prs := []history.MergedPullRequest{pr("fix auth refresh", "2026-08-19T10:00:00Z", "alice")}
	item := g.digest(context.Background(), "org/repo", "2026-08-19", prs, map[string]int{"2026-08-19": 1})
	if item.Source != "digest" || item.PRCount != 1 || !strings.HasPrefix(item.TLDR, "Merged fix auth refresh") {
		t.Fatalf("item = %+v, want digest fallback", item)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "news llm org/repo 2026-08-19") {
		t.Fatalf("logged = %v, want one LLM error line", logged)
	}

	// With Logf unset the error path must still fall back silently.
	g.Logf = nil
	if item := g.digest(context.Background(), "org/repo", "2026-08-19", prs, nil); item.Source != "digest" {
		t.Fatalf("item without Logf = %+v, want digest fallback", item)
	}
}

func TestFallbackTLDREdgeCases(t *testing.T) {
	if got := FallbackTLDR(nil); got != "Merged recent PRs." {
		t.Fatalf("FallbackTLDR(nil) = %q", got)
	}
	got := FallbackTLDR([]history.MergedPullRequest{{Title: "   "}})
	if got != "Merged untitled PR." {
		t.Fatalf("FallbackTLDR(blank title) = %q", got)
	}
	if got := joinTitles([]string{"only one"}); got != "only one" {
		t.Fatalf("joinTitles(single) = %q", got)
	}
}

func TestGroupRecentPRsSkipsOutsideWindow(t *testing.T) {
	now := newsNow()
	prs := []history.MergedPullRequest{
		pr("too old", now.AddDate(0, 0, -WindowDays).Format(time.RFC3339), "a"),
		pr("in window", now.AddDate(0, 0, -1).Format(time.RFC3339), "b"),
		pr("future", now.AddDate(0, 0, 1).Format(time.RFC3339), "c"),
		pr("today", now.Format(time.RFC3339), "d"),
	}
	grouped := groupRecentPRs(prs, now)
	if len(grouped) != 2 {
		t.Fatalf("grouped dates = %v, want 2 (yesterday and today)", grouped)
	}
	for date, got := range grouped {
		if len(got) != 1 || got[0].Title == "too old" || got[0].Title == "future" {
			t.Fatalf("date %s grouped %+v, want exactly one in-window PR", date, got)
		}
	}
}

func TestEnsurePeriodBlank(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t"} {
		if got := ensurePeriod(in); got != "" {
			t.Fatalf("ensurePeriod(%q) = %q, want empty", in, got)
		}
	}
	if got := ensurePeriod("done?"); got != "done?" {
		t.Fatalf("ensurePeriod(question) = %q", got)
	}
}
