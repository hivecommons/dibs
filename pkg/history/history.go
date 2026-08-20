// Package history backfills public GitHub activity for hive-managed repos and
// persists the aggregate daily counts that enrich Dibs repo index charts.
package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubestellar/dibs/pkg/registry"
)

const (
	defaultBaseURL = "https://api.github.com"
	windowDays     = 30
	maxPullPages   = 3
	perPage        = 100
	requestTimeout = 10 * time.Second
	maxBodyBytes   = 4 << 20
	maxConcurrent  = 4
)

// DayActivity is one UTC calendar day's public GitHub activity for a repo.
type DayActivity struct {
	Date       string `json:"date"`
	MergedPRs  int    `json:"mergedPRs"`
	Commits    int    `json:"commits"`
	Backfilled bool   `json:"backfilled"`
}

// RepoHistory is the persisted aggregate history for one repo.
type RepoHistory struct {
	RepoID    string        `json:"repoID"`
	Days      []DayActivity `json:"days"`
	FetchedAt time.Time     `json:"fetchedAt"`
}

// Store persists repo histories under DATA_DIR with atomic writes.
type Store struct {
	mu   sync.RWMutex
	path string
	data map[string]RepoHistory
}

// NewStore opens dir/repo-history.json, creating an empty store if absent.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("history: creating data dir: %w", err)
	}
	s := &Store{path: filepath.Join(dir, "repo-history.json"), data: map[string]RepoHistory{}}
	raw, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("history: reading repo-history.json: %w", err)
	default:
		var histories []RepoHistory
		if err := json.Unmarshal(raw, &histories); err != nil {
			return nil, fmt.Errorf("history: corrupt repo-history.json: %w", err)
		}
		for _, h := range histories {
			s.data[h.RepoID] = normalizeHistory(h)
		}
	}
	return s, nil
}

// List returns a copy of all persisted histories.
func (s *Store) List() []RepoHistory {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RepoHistory, 0, len(s.data))
	for _, h := range s.data {
		out = append(out, copyHistory(h))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepoID < out[j].RepoID })
	return out
}

// Get returns a copy of one repo's history.
func (s *Store) Get(repoID string) (RepoHistory, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.data[repoID]
	if !ok {
		return RepoHistory{}, false
	}
	return copyHistory(h), true
}

// Upsert replaces the stored days for repoID. It is idempotent for identical
// day counts, so repeated backfills cannot double-apply chart movement.
func (s *Store) Upsert(repoID string, days []DayActivity, fetchedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := normalizeHistory(RepoHistory{RepoID: repoID, Days: days, FetchedAt: fetchedAt.UTC()})
	s.data[repoID] = h
	return s.persistLocked()
}

func (s *Store) persistLocked() error {
	histories := make([]RepoHistory, 0, len(s.data))
	for _, h := range s.data {
		histories = append(histories, h)
	}
	sort.Slice(histories, func(i, j int) bool { return histories[i].RepoID < histories[j].RepoID })
	return atomicWriteJSON(s.path, histories)
}

func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("history: marshaling: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-history-*")
	if err != nil {
		return fmt.Errorf("history: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("history: writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("history: closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("history: renaming temp file: %w", err)
	}
	return nil
}

func copyHistory(h RepoHistory) RepoHistory {
	h.Days = append([]DayActivity(nil), h.Days...)
	return h
}

func normalizeHistory(h RepoHistory) RepoHistory {
	byDate := map[string]DayActivity{}
	for _, d := range h.Days {
		if d.Date == "" {
			continue
		}
		d.Backfilled = true
		byDate[d.Date] = d
	}
	h.Days = h.Days[:0]
	for _, d := range byDate {
		h.Days = append(h.Days, d)
	}
	sort.Slice(h.Days, func(i, j int) bool { return h.Days[i].Date < h.Days[j].Date })
	return h
}

// Backfiller fetches GitHub activity and stores aggregate daily counts.
type Backfiller struct {
	Store   *Store
	BaseURL string
	Token   string
	Client  *http.Client
	Now     func() time.Time
	Logf    func(string, ...any)

	sem    chan struct{}
	mu     sync.Mutex
	active map[string]bool
}

// NewBackfiller returns a production backfiller.
func NewBackfiller(store *Store, token string) *Backfiller {
	return &Backfiller{
		Store:  store,
		Token:  token,
		Client: &http.Client{Timeout: requestTimeout},
		Logf:   log.Printf,
	}
}

// RefreshAsync starts a bounded, non-blocking refresh for repos that have no
// history or whose last successful fetch is from a previous UTC day.
func (b *Backfiller) RefreshAsync(repos []registry.RepoProfile) {
	if b == nil || b.Store == nil {
		return
	}
	for _, rp := range repos {
		repoID := rp.RepoID
		if !b.needsRefresh(repoID) || !b.markActive(repoID) {
			continue
		}
		go func() {
			defer b.clearActive(repoID)
			if err := b.Backfill(context.Background(), repoID); err != nil && b.Logf != nil {
				b.Logf("history backfill %s: %v", repoID, err)
			}
		}()
	}
}

func (b *Backfiller) needsRefresh(repoID string) bool {
	h, ok := b.Store.Get(repoID)
	if !ok || len(h.Days) == 0 {
		return true
	}
	now := b.now().UTC().Truncate(24 * time.Hour)
	return h.FetchedAt.UTC().Truncate(24 * time.Hour).Before(now)
}

func (b *Backfiller) markActive(repoID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil {
		b.active = map[string]bool{}
	}
	if b.sem == nil {
		b.sem = make(chan struct{}, maxConcurrent)
	}
	if b.active[repoID] {
		return false
	}
	b.active[repoID] = true
	return true
}

func (b *Backfiller) clearActive(repoID string) {
	b.mu.Lock()
	delete(b.active, repoID)
	b.mu.Unlock()
}

// Backfill fetches trailing GitHub activity for repoID and replaces its stored
// daily counts. Rate limits and 202 stats responses are graceful skips.
func (b *Backfiller) Backfill(ctx context.Context, repoID string) error {
	if b == nil || b.Store == nil {
		return nil
	}
	select {
	case b.semaphore() <- struct{}{}:
		defer func() { <-b.semaphore() }()
	case <-ctx.Done():
		return ctx.Err()
	}

	days := zeroDays(b.now())
	prs, err := b.fetchMergedPRs(ctx, repoID)
	if err != nil {
		return err
	}
	commits, err := b.fetchCommitActivity(ctx, repoID)
	if errors.Is(err, errSkipRepo) {
		return err
	}
	if err != nil && !errors.Is(err, errStatsPending) {
		return err
	}
	for date, n := range prs {
		if d, ok := days[date]; ok {
			d.MergedPRs = n
			days[date] = d
		}
	}
	for date, n := range commits {
		if d, ok := days[date]; ok {
			d.Commits = n
			days[date] = d
		}
	}
	return b.Store.Upsert(repoID, sortedDays(days), b.now())
}

func (b *Backfiller) semaphore() chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sem == nil {
		b.sem = make(chan struct{}, maxConcurrent)
	}
	return b.sem
}

func (b *Backfiller) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now().UTC()
}

func zeroDays(now time.Time) map[string]DayActivity {
	out := map[string]DayActivity{}
	start := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(windowDays - 1))
	for i := 0; i < windowDays; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		out[date] = DayActivity{Date: date, Backfilled: true}
	}
	return out
}

func sortedDays(days map[string]DayActivity) []DayActivity {
	out := make([]DayActivity, 0, len(days))
	for _, d := range days {
		d.Backfilled = true
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func (b *Backfiller) fetchMergedPRs(ctx context.Context, repoID string) (map[string]int, error) {
	out := map[string]int{}
	for page := 1; page <= maxPullPages; page++ {
		path := fmt.Sprintf("/repos/%s/pulls?state=closed&sort=updated&direction=desc&per_page=%d&page=%d", repoID, perPage, page)
		var pulls []struct {
			MergedAt *time.Time `json:"merged_at"`
		}
		if err := b.getJSON(ctx, path, &pulls); err != nil {
			return nil, err
		}
		for _, pr := range pulls {
			if pr.MergedAt == nil {
				continue
			}
			out[pr.MergedAt.UTC().Format("2006-01-02")]++
		}
		if len(pulls) < perPage {
			break
		}
	}
	return out, nil
}

func (b *Backfiller) fetchCommitActivity(ctx context.Context, repoID string) (map[string]int, error) {
	var weeks []struct {
		Week int64 `json:"week"`
		Days []int `json:"days"`
	}
	if err := b.getJSON(ctx, fmt.Sprintf("/repos/%s/stats/commit_activity", repoID), &weeks); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, w := range weeks {
		start := time.Unix(w.Week, 0).UTC()
		for i, n := range w.Days {
			date := start.AddDate(0, 0, i).Format("2006-01-02")
			out[date] += n
		}
	}
	return out, nil
}

var (
	errSkipRepo     = errors.New("skip repo")
	errStatsPending = errors.New("github stats pending")
)

func (b *Backfiller) getJSON(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	base := b.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if shouldSkip(resp) {
		return errSkipRepo
	}
	if resp.StatusCode == http.StatusAccepted {
		return errStatsPending
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github returned status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(out)
}

func shouldSkip(resp *http.Response) bool {
	return resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden &&
			(resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"))
}
