// Package registry maintains profiles of hive-managed repositories and their
// "accepting ideas" opt-in. Profiles are synced from the hub (via a HubClient
// interface with a fake for tests — the hub-side endpoint is a small
// follow-up, contract below) and/or seeded from a static JSON file
// (REPOS_SEED_FILE) for dev/demo. Owner-editable fields (acceptingIdeas,
// topics, appetite) persist locally in DATA_DIR and survive syncs.
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
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// MaxAppetiteLen caps the free-text appetite note.
const MaxAppetiteLen = 500

// MaxTopics caps the topics list.
const MaxTopics = 20

// RepoProfile is one hive-managed repository's dibs profile.
type RepoProfile struct {
	RepoID      string `json:"repoID"` // org/name
	HiveID      string `json:"hiveID"`
	Owner       string `json:"owner"` // identity key of the hive owner
	Description string `json:"description,omitempty"`
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
	BaseURL string
	Client  *http.Client
}

// ReposPath is the hub endpoint listing hive-managed repos.
const ReposPath = "/api/saas/dibs/repos"

const hubRequestTimeout = 10 * time.Second

// maxReposBody bounds the hub response we are willing to parse.
const maxReposBody = 4 << 20

// ListRepos implements HubClient.
func (c *HTTPHubClient) ListRepos(ctx context.Context) ([]RepoProfile, error) {
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
	return repos, nil
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
	return r, nil
}

// persistLocked writes repos.json atomically. Caller holds r.mu.
func (r *Registry) persistLocked() error {
	repos := make([]RepoProfile, 0, len(r.repos))
	for _, rp := range r.repos {
		repos = append(repos, *rp)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].RepoID < repos[j].RepoID })
	data, err := json.MarshalIndent(repos, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: marshaling: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".tmp-repos-*")
	if err != nil {
		return fmt.Errorf("registry: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("registry: writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("registry: closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("registry: renaming temp file: %w", err)
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
		if in.Topics == nil {
			in.Topics = []string{}
		}
		if existing, ok := r.repos[in.RepoID]; ok {
			in.AcceptingIdeas = existing.AcceptingIdeas
			in.Topics = existing.Topics
			in.Appetite = existing.Appetite
			in.PassedIdeas = existing.PassedIdeas
		}
		r.repos[in.RepoID] = &in
	}
	return r.persistLocked()
}

// Sync pulls the hub's repo list and merges it.
func (r *Registry) Sync(ctx context.Context, hub HubClient) error {
	repos, err := hub.ListRepos(ctx)
	if err != nil {
		return err
	}
	return r.Merge(repos)
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
		if in.Topics == nil {
			in.Topics = []string{}
		}
		if _, ok := r.repos[in.RepoID]; ok {
			continue // never clobber a known repo's local edits
		}
		r.repos[in.RepoID] = &in
	}
	return r.persistLocked()
}

// Get returns a copy of one profile.
func (r *Registry) Get(repoID string) (*RepoProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rp, ok := r.repos[repoID]
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
		if rp.Owner == owner {
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
	rp, ok := r.repos[repoID]
	if !ok {
		return nil, ErrNotFound
	}
	if rp.Owner != actor {
		return nil, ErrForbidden
	}
	if upd.Topics != nil && len(*upd.Topics) > MaxTopics {
		return nil, fmt.Errorf("registry: more than %d topics", MaxTopics)
	}
	if upd.Appetite != nil && len(*upd.Appetite) > MaxAppetiteLen {
		return nil, fmt.Errorf("registry: appetite exceeds %d characters", MaxAppetiteLen)
	}
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
	rp, ok := r.repos[repoID]
	if !ok {
		return ErrNotFound
	}
	if rp.Owner != actor {
		return ErrForbidden
	}
	if rp.HasPassed(ideaID) {
		return nil
	}
	rp.PassedIdeas = append(rp.PassedIdeas, ideaID)
	return r.persistLocked()
}
