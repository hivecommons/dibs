package api

import (
	"testing"

	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

func TestMatchNotifierNewMatch(t *testing.T) {
	repo := &registry.RepoProfile{RepoID: "org/repo", Owner: "olivia"}

	t.Run("nil store is a no-op", func(t *testing.T) {
		n := &MatchNotifier{}
		n.NewMatch("alice", "olivia", &store.Idea{ID: "i1", Title: "T", Visibility: store.VisibilityPublic}, repo, 0.9)
	})

	newNotifier := func(t *testing.T) (*MatchNotifier, *notify.Store) {
		t.Helper()
		ns, err := notify.New(t.TempDir())
		if err != nil {
			t.Fatalf("notify.New: %v", err)
		}
		return &MatchNotifier{Notify: ns}, ns
	}

	t.Run("public idea notifies author and repo owner", func(t *testing.T) {
		n, ns := newNotifier(t)
		idea := &store.Idea{ID: "i1", Title: "Great idea", Visibility: store.VisibilityPublic}
		n.NewMatch("alice", "olivia", idea, repo, 0.9)

		author := ns.ListByUser("alice", false)
		if len(author) != 1 || author[0].Kind != notify.KindMatch || author[0].IdeaID != "i1" || author[0].RepoID != "org/repo" {
			t.Fatalf("author notifications = %+v", author)
		}
		owner := ns.ListByUser("olivia", false)
		if len(owner) != 1 || owner[0].Kind != notify.KindMatch {
			t.Fatalf("owner notifications = %+v", owner)
		}
	})

	t.Run("private idea never notifies repo owner", func(t *testing.T) {
		n, ns := newNotifier(t)
		idea := &store.Idea{ID: "i2", Title: "Secret idea", Visibility: store.VisibilityPrivate}
		n.NewMatch("alice", "olivia", idea, repo, 0.9)

		if got := ns.ListByUser("alice", false); len(got) != 1 {
			t.Fatalf("author notifications = %+v, want 1", got)
		}
		if got := ns.ListByUser("olivia", false); len(got) != 0 {
			t.Fatalf("private idea leaked to repo owner: %+v", got)
		}
	})

	t.Run("self-match notifies only once", func(t *testing.T) {
		n, ns := newNotifier(t)
		idea := &store.Idea{ID: "i3", Title: "Own repo idea", Visibility: store.VisibilityPublic}
		n.NewMatch("olivia", "olivia", idea, repo, 0.9)

		if got := ns.ListByUser("olivia", false); len(got) != 1 {
			t.Fatalf("self-match notifications = %+v, want exactly 1", got)
		}
	})
}
