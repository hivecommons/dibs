// Package registry maintains profiles of hive-managed repositories and their
// "accepting ideas" opt-in. Profiles are synced from the hub (via a HubClient
// interface with a fake for tests — the hub-side endpoint is a small
// follow-up, contract below) and/or seeded from a static JSON file
// (REPOS_SEED_FILE) for dev/demo. Locally assigned ticker symbols and
// owner-editable fields (acceptingIdeas, topics, appetite) persist locally
// in DATA_DIR and survive syncs.
//
// Hub contract (follow-up):
//
//	GET {HUB_URL}/api/saas/dibs/repos
//
// returns 200 with
//
//	[{"repoID":"org/name","hiveID":"...","owner":"github-login","description":"..."}]
//
// listing every repo managed by a hive, with the hive owner's identity key.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hivecommons/dibs/pkg/fsutil"
)

// MaxAppetiteLen caps the free-text appetite note.
const MaxAppetiteLen = 500

// MaxTopics caps the topics list.
const MaxTopics = 20

// MaxTopicLen caps a single topic. Matches GitHub's own topic limit, and
// keeps owner-supplied text from inflating the public repo listing and the
// per-idea match prompt (which embeds topics untruncated).
const MaxTopicLen = 50

// RepoProfile is one hive-managed repository's dibs profile.
type RepoProfile struct {
	RepoID      string `json:"repoID"` // org/name
	HiveID      string `json:"hiveID"`
	Owner       string `json:"owner"` // identity key of the hive owner
	Description string `json:"description,omitempty"`
	// Symbol is the locally assigned, stable 4-letter exchange ticker for
	// the repo. It is assigned once and preserved across hub syncs.
	Symbol string `json:"symbol,omitempty"`
	// ContributeURL is the hive's public /contribute page (ClankeR, the
	// contributor relay), hub-fed like RepoID/HiveID/Owner. Empty when the
	// hive has reported no public base.
	ContributeURL  string   `json:"contributeURL,omitempty"`
	Topics         []string `json:"topics"`
	AcceptingIdeas bool     `json:"acceptingIdeas"`
	// Appetite is the owner's free-text note on what kinds of ideas the repo
	// is hungry for.
	Appetite string `json:"appetite,omitempty"`
	// PassedIdeas are idea IDs the repo owner swiped away; they never
	// resurface in this repo's candidate feed. Local-only, like the other
	// owner fields.
	PassedIdeas []string `json:"passedIdeas,omitempty"`
}

// HasPassed reports whether the repo owner swiped ideaID away.
func (rp *RepoProfile) HasPassed(ideaID string) bool {
	for _, id := range rp.PassedIdeas {
		if id == ideaID {
			return true
		}
	}
	return false
}

// ErrNotFound is returned when a repo profile does not exist.
var ErrNotFound = errors.New("registry: repo not found")

// ErrForbidden is returned when a non-owner tries to edit a profile.
var ErrForbidden = errors.New("registry: not the repo owner")

// HubClient lists the hub's hive-managed repos.
type HubClient interface {
	ListRepos(ctx context.Context) ([]RepoProfile, error)
}

// HTTPHubClient is the production HubClient (codes against the follow-up hub
// endpoint documented in the package comment).
type HTTPHubClient struct {
	BaseURL   string
	Client    *http.Client
	Token     string
	GitHubAPI string

	// enrichMu guards looked and notBefore. looked remembers repos whose
	// GitHub lookup already completed (even with an empty description or a
	// non-200) so each sync does not spend the anonymous 60 req/hr bucket,
	// which settlement verification shares, retrying them. notBefore is the
	// rate-limit reset time before which enrichment is skipped.
	enrichMu  sync.Mutex
	looked    map[string]bool
	notBefore time.Time
}

// descriptionSkipper is implemented by hub clients that can avoid enriching
// repos the registry already has a description for.
type descriptionSkipper interface {
	ListReposSkippingDescriptions(ctx context.Context, known func(repoID string) bool) ([]RepoProfile, error)
}

// ReposPath is the hub endpoint listing hive-managed repos.
const ReposPath = "/api/saas/dibs/repos"

const hubRequestTimeout = 10 * time.Second

// maxReposBody bounds the hub response we are willing to parse.
const maxReposBody = 4 << 20

// ListRepos implements HubClient.
func (c *HTTPHubClient) ListRepos(ctx context.Context) ([]RepoProfile, error) {
	return c.ListReposSkippingDescriptions(ctx, nil)
}

// ListReposSkippingDescriptions is ListRepos, except repos for which known
// returns true are not enriched from GitHub. known may be nil.
func (c *HTTPHubClient) ListReposSkippingDescriptions(ctx context.Context, known func(repoID string) bool) ([]RepoProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+ReposPath, nil)
	if err != nil {
		return nil, fmt.Errorf("registry: building hub request: %w", err)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: hubRequestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry: hub unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry: hub returned status %d", resp.StatusCode)
	}
	var repos []RepoProfile
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReposBody)).Decode(&repos); err != nil {
		return nil, fmt.Errorf("registry: decoding hub response: %w", err)
	}
	c.enrichDescriptions(ctx, repos, known)
	return repos, nil
}

func (c *HTTPHubClient) enrichDescriptions(ctx context.Context, repos []RepoProfile, known func(repoID string) bool) {
	c.enrichMu.Lock()
	wait := time.Now().Before(c.notBefore)
	c.enrichMu.Unlock()
	if wait {
		return
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: hubRequestTimeout}
	}
	base := strings.TrimRight(c.GitHubAPI, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	for i := range repos {
		if strings.TrimSpace(repos[i].Description) != "" || !strings.Contains(repos[i].RepoID, "/") {
			continue
		}
		if known != nil && known(repos[i].RepoID) {
			continue
		}
		c.enrichMu.Lock()
		done := c.looked[repos[i].RepoID]
		c.enrichMu.Unlock()
		if done {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/"+repos[i].RepoID, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			slog.Warn("registry: GitHub refused description lookup; skipping enrichment this sync", "status", resp.StatusCode)
			// A secondary limit can lift before the primary window resets.
			if secs, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("Retry-After")), 10, 64); err == nil && secs >= 0 {
				c.enrichMu.Lock()
				c.notBefore = time.Now().Add(time.Duration(secs) * time.Second)
				c.enrichMu.Unlock()
			} else if reset, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
				c.enrichMu.Lock()
				c.notBefore = time.Unix(reset, 0)
				c.enrichMu.Unlock()
			}
			return
		}
		c.enrichMu.Lock()
		if c.looked == nil {
			c.looked = map[string]bool{}
		}
		c.looked[repos[i].RepoID] = true
		c.enrichMu.Unlock()
		var gh struct {
			Description string `json:"description"`
		}
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&gh)
		}
		resp.Body.Close()
		if strings.TrimSpace(gh.Description) != "" {
			repos[i].Description = strings.TrimSpace(gh.Description)
		}
	}
}

// FakeHub is an in-memory HubClient for tests and local development.
type FakeHub struct{ Repos []RepoProfile }

// ListRepos implements HubClient.
func (f *FakeHub) ListRepos(context.Context) ([]RepoProfile, error) { return f.Repos, nil }

// Registry is the mutex-guarded, file-persisted set of repo profiles.
type Registry struct {
	mu    sync.RWMutex
	path  string // repos.json under DATA_DIR
	repos map[string]*RepoProfile
}

// New opens (creating if needed) a registry persisted at dir/repos.json.
func New(dir string) (*Registry, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("registry: creating data dir: %w", err)
	}
	r := &Registry{path: filepath.Join(dir, "repos.json"), repos: map[string]*RepoProfile{}}
	raw, err := os.ReadFile(r.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// fresh registry
	case err != nil:
		return nil, fmt.Errorf("registry: reading repos.json: %w", err)
	default:
		var repos []RepoProfile
		if err := json.Unmarshal(raw, &repos); err != nil {
			return nil, fmt.Errorf("registry: corrupt repos.json: %w", err)
		}
		for i := range repos {
			rp := repos[i]
			r.repos[rp.RepoID] = &rp
		}
	}
	if r.ensureRepoSymbolsLocked() {
		if err := r.persistLocked(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// persistLocked writes repos.json atomically. Caller holds r.mu.
func (r *Registry) persistLocked() error {
	repos := make([]RepoProfile, 0, len(r.repos))
	for _, rp := range r.repos {
		repos = append(repos, *rp)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].RepoID < repos[j].RepoID })
	if err := fsutil.AtomicWriteJSON(r.path, repos); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	return nil
}

// Merge upserts hub/seed-sourced profiles, PRESERVING locally-edited
// owner fields (acceptingIdeas, topics, appetite) on repos we already know.
func (r *Registry) Merge(incoming []RepoProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range incoming {
		in := incoming[i]
		in.Symbol = "" // symbols are assigned locally, never accepted from hub input
		in.ContributeURL = sanitizeContributeURL(in.ContributeURL)
		if in.Topics == nil {
			in.Topics = []string{}
		}
		if existing, ok := r.lookupLocked(in.RepoID); ok {
			in.RepoID = existing.RepoID // keep the stored spelling
			in.Symbol = existing.Symbol
			if strings.TrimSpace(in.Description) == "" {
				in.Description = existing.Description
			}
			in.AcceptingIdeas = existing.AcceptingIdeas
			in.Topics = existing.Topics
			in.Appetite = existing.Appetite
			in.PassedIdeas = existing.PassedIdeas
		}
		r.repos[in.RepoID] = &in
	}
	r.ensureRepoSymbolsLocked()
	return r.persistLocked()
}

// sanitizeContributeURL keeps only absolute http(s) URLs. ContributeURL is
// rendered verbatim as a link target and encoded into the public QR code,
// so anything else (javascript:, data:, relative paths, garbage) is dropped
// and the callers fall back to the hive directory.
func sanitizeContributeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return raw
	}
	return ""
}

// Sync pulls the hub's repo list and merges it.
func (r *Registry) Sync(ctx context.Context, hub HubClient) error {
	var repos []RepoProfile
	var err error
	if s, ok := hub.(descriptionSkipper); ok {
		repos, err = s.ListReposSkippingDescriptions(ctx, r.hasDescription)
	} else {
		repos, err = hub.ListRepos(ctx)
	}
	if err != nil {
		return err
	}
	return r.Merge(repos)
}

// hasDescription reports whether the stored profile already has a description.
func (r *Registry) hasDescription(repoID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	existing, ok := r.lookupLocked(repoID)
	return ok && strings.TrimSpace(existing.Description) != ""
}

// LoadSeedFile merges profiles from a static JSON seed file (REPOS_SEED_FILE).
// Unlike Merge, seed values for owner fields apply on first sight only —
// local edits still win on repos already in the registry.
func (r *Registry) LoadSeedFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("registry: reading seed file: %w", err)
	}
	var repos []RepoProfile
	if err := json.Unmarshal(raw, &repos); err != nil {
		return fmt.Errorf("registry: parsing seed file: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range repos {
		in := repos[i]
		in.Symbol = "" // seed files cannot reserve or override ticker symbols
		in.ContributeURL = sanitizeContributeURL(in.ContributeURL)
		if in.Topics == nil {
			in.Topics = []string{}
		}
		if _, ok := r.lookupLocked(in.RepoID); ok {
			continue // never clobber a known repo's local edits
		}
		r.repos[in.RepoID] = &in
	}
	r.ensureRepoSymbolsLocked()
	return r.persistLocked()
}

// lookupLocked resolves repoID to a stored profile. GitHub treats
// "org/name" case-insensitively, so an exact-key miss falls back to a
// case-folded match; the returned profile carries the canonical RepoID.
// Caller holds r.mu.
func (r *Registry) lookupLocked(repoID string) (*RepoProfile, bool) {
	if rp, ok := r.repos[repoID]; ok {
		return rp, true
	}
	for id, rp := range r.repos {
		if strings.EqualFold(id, repoID) {
			return rp, true
		}
	}
	return nil, false
}

// Get returns a copy of one profile. repoID is matched case-insensitively;
// the copy's RepoID is the registered spelling.
func (r *Registry) Get(repoID string) (*RepoProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rp, ok := r.lookupLocked(repoID)
	if !ok {
		return nil, ErrNotFound
	}
	cp := *rp
	return &cp, nil
}

// List returns all profiles; if acceptingOnly, just those opted in.
func (r *Registry) List(acceptingOnly bool) []RepoProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RepoProfile, 0, len(r.repos))
	for _, rp := range r.repos {
		if acceptingOnly && !rp.AcceptingIdeas {
			continue
		}
		out = append(out, *rp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepoID < out[j].RepoID })
	return out
}

// ListByOwner returns the profiles owned by the given identity.
func (r *Registry) ListByOwner(owner string) []RepoProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []RepoProfile{}
	for _, rp := range r.repos {
		if strings.EqualFold(rp.Owner, owner) {
			out = append(out, *rp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepoID < out[j].RepoID })
	return out
}

// OwnerUpdate is the owner-editable subset of a profile. Nil pointers mean
// "leave unchanged".
type OwnerUpdate struct {
	AcceptingIdeas *bool     `json:"acceptingIdeas,omitempty"`
	Topics         *[]string `json:"topics,omitempty"`
	Appetite       *string   `json:"appetite,omitempty"`
}

// ApplyOwnerUpdate mutates owner-editable fields, enforcing that actor IS the
// repo's owner. Returns ErrForbidden otherwise.
func (r *Registry) ApplyOwnerUpdate(repoID, actor string, upd OwnerUpdate) (*RepoProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rp, ok := r.lookupLocked(repoID)
	if !ok {
		return nil, ErrNotFound
	}
	if !strings.EqualFold(rp.Owner, actor) {
		return nil, ErrForbidden
	}
	if upd.Topics != nil {
		if len(*upd.Topics) > MaxTopics {
			return nil, fmt.Errorf("registry: more than %d topics", MaxTopics)
		}
		for _, t := range *upd.Topics {
			if strings.TrimSpace(t) == "" {
				return nil, errors.New("registry: topics cannot be blank")
			}
			if utf8.RuneCountInString(t) > MaxTopicLen {
				return nil, fmt.Errorf("registry: topic exceeds %d characters", MaxTopicLen)
			}
		}
	}
	if upd.Appetite != nil && utf8.RuneCountInString(*upd.Appetite) > MaxAppetiteLen {
		return nil, fmt.Errorf("registry: appetite exceeds %d characters", MaxAppetiteLen)
	}
	prevAccepting, prevTopics, prevAppetite := rp.AcceptingIdeas, rp.Topics, rp.Appetite
	if upd.AcceptingIdeas != nil {
		rp.AcceptingIdeas = *upd.AcceptingIdeas
	}
	if upd.Topics != nil {
		rp.Topics = *upd.Topics
	}
	if upd.Appetite != nil {
		rp.Appetite = *upd.Appetite
	}
	if err := r.persistLocked(); err != nil {
		rp.AcceptingIdeas, rp.Topics, rp.Appetite = prevAccepting, prevTopics, prevAppetite
		return nil, err
	}
	cp := *rp
	return &cp, nil
}

// AddPassedIdea records the owner swiping an idea away from repoID's feed.
// Idempotent. Enforces that actor is the repo's owner.
func (r *Registry) AddPassedIdea(repoID, actor, ideaID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rp, ok := r.lookupLocked(repoID)
	if !ok {
		return ErrNotFound
	}
	if !strings.EqualFold(rp.Owner, actor) {
		return ErrForbidden
	}
	if rp.HasPassed(ideaID) {
		return nil
	}
	prev := rp.PassedIdeas
	rp.PassedIdeas = append(prev[:len(prev):len(prev)], ideaID)
	if err := r.persistLocked(); err != nil {
		rp.PassedIdeas = prev
		return err
	}
	return nil
}
