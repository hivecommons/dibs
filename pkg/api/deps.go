package api

import (
	"context"
	"time"

	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/news"
	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

// The interfaces below are the narrow seams pkg/api actually calls through.
// API depends on these instead of concrete *struct pointers into sibling
// packages, so each handler group's real dependency surface is visible and
// independently mockable. The concrete types in pkg/store, pkg/registry,
// pkg/history, pkg/news, pkg/match, and pkg/settle all satisfy them
// unchanged — this is pure encapsulation, no behavior change.

// IdeaStore is the idea-persistence surface pkg/api uses.
type IdeaStore interface {
	Create(idea *store.Idea) error
	Get(id string) (*store.Idea, error)
	Update(idea *store.Idea) error
	Mutate(id string, touch bool, fn func(*store.Idea) error) (*store.Idea, error)
	Transition(id, to string) (*store.Idea, error)
	Delete(id string) error
	ListByAuthor(author string) ([]*store.Idea, error)
	ListPublic() ([]*store.Idea, error)
	ListOfferedTo(repoIDs []string) ([]*store.Idea, error)
	ListAll() ([]*store.Idea, error)
	ListSettled() ([]*store.Idea, error)
}

// RepoRegistry is the repo-registry surface pkg/api uses.
type RepoRegistry interface {
	Get(repoID string) (*registry.RepoProfile, error)
	List(acceptingOnly bool) []registry.RepoProfile
	ListByOwner(owner string) []registry.RepoProfile
	ApplyOwnerUpdate(repoID, actor string, upd registry.OwnerUpdate) (*registry.RepoProfile, error)
	AddPassedIdea(repoID, actor, ideaID string) error
	Merge(incoming []registry.RepoProfile) error
}

// RepoHistory is the per-repo activity-history surface pkg/api uses.
type RepoHistory interface {
	Get(repoID string) (history.RepoHistory, bool)
}

// RepoNews is the cached-news surface pkg/api uses.
type RepoNews interface {
	Get(repoID string) []news.Item
}

// Matcher is the idea<->repo match-engine surface pkg/api uses. A nil
// Matcher means matching is disabled (Wave-2 features degrade).
type Matcher interface {
	EnsureTLDR(ctx context.Context, idea *store.Idea) (string, error)
	MatchesForIdea(ctx context.Context, idea *store.Idea) ([]store.Match, error)
	CNCFMatchesForIdea(ctx context.Context, idea *store.Idea) ([]match.CNCFMatch, error)
	ScoreForRepo(ctx context.Context, idea *store.Idea, rp *registry.RepoProfile) (store.Match, error)
	Refine(ctx context.Context, title, body string, repo *registry.RepoProfile) *match.RefinedDraft
	RematchIdea(ctx context.Context, idea *store.Idea, persist bool, progress match.ProgressFunc) (string, []store.Match, []match.CNCFMatch, error)
	PersistRematchResults(ideaID string, expectedUpdatedAt time.Time, tldr string, matches []store.Match, cncf []match.CNCFMatch) error
}

// IdeaSettler opens the credited GitHub issue on accept. A nil IdeaSettler
// records accepts without opening issues.
type IdeaSettler interface {
	Settle(ctx context.Context, idea *store.Idea, repoID string) (string, error)
	HasGitHub() bool
}

// Notifier is the in-app notification surface pkg/api uses. A nil Notifier
// disables notifications.
type Notifier interface {
	Add(user, kind, message, ideaID, repoID string) error
	ListByUser(user string, unreadOnly bool) []notify.Notification
	MarkRead(user string, ids []string, all bool) error
}

// NewFromConfig builds an API from the concrete, possibly-nil dependency
// pointers callers wire up (mirrors server.Config). It exists because a nil
// *T assigned directly into an interface field produces a non-nil interface
// wrapping a nil pointer — every "a.Engine == nil"-style disabled-feature
// check in pkg/api relies on the field being a true nil interface, so nil
// concrete pointers must be left unset rather than assigned.
func NewFromConfig(st IdeaStore, reg RepoRegistry, hist *history.Store, nw *news.Store, eng *match.Engine, settler *settle.Settler, ntf *notify.Store) *API {
	a := &API{Store: st, Registry: reg}
	if hist != nil {
		a.History = hist
	}
	if nw != nil {
		a.News = nw
	}
	if eng != nil {
		a.Engine = eng
	}
	if settler != nil {
		a.Settler = settler
	}
	if ntf != nil {
		a.Notify = ntf
	}
	return a
}
