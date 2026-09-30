package api

import (
	"testing"

	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/news"
	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/settle"
)

// TestNewFromConfig_NilPointersStayNilInterfaces pins the typed-nil guard
// NewFromConfig exists for: a nil *T assigned straight into an interface
// field would make "a.Engine == nil"-style disabled-feature checks fail,
// so nil concrete pointers must leave the interface fields truly unset.
func TestNewFromConfig_NilPointersStayNilInterfaces(t *testing.T) {
	a := NewFromConfig(nil, nil, nil, nil, nil, nil, nil)
	if a == nil {
		t.Fatal("NewFromConfig returned nil API")
	}
	if a.History != nil {
		t.Errorf("History: nil *history.Store produced non-nil interface %#v", a.History)
	}
	if a.News != nil {
		t.Errorf("News: nil *news.Store produced non-nil interface %#v", a.News)
	}
	if a.Engine != nil {
		t.Errorf("Engine: nil *match.Engine produced non-nil interface %#v", a.Engine)
	}
	if a.Settler != nil {
		t.Errorf("Settler: nil *settle.Settler produced non-nil interface %#v", a.Settler)
	}
	if a.Notify != nil {
		t.Errorf("Notify: nil *notify.Store produced non-nil interface %#v", a.Notify)
	}
}

// TestNewFromConfig_NonNilDepsAreWired covers the wiring half: every
// non-nil concrete dependency must land on the matching interface field.
func TestNewFromConfig_NonNilDepsAreWired(t *testing.T) {
	base, _ := newAPIFixture(t)

	hist, err := history.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("history.NewStore: %v", err)
	}
	nw, err := news.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("news.NewStore: %v", err)
	}
	ntf, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	eng := &match.Engine{}
	settler := &settle.Settler{GitHub: &settle.Fake{}}

	a := NewFromConfig(base.Store, base.Registry, hist, nw, eng, settler, ntf)
	if a.Store == nil || a.Registry == nil {
		t.Fatal("Store/Registry not wired")
	}
	if a.History != RepoHistory(hist) {
		t.Errorf("History not wired: got %#v", a.History)
	}
	if a.News != RepoNews(nw) {
		t.Errorf("News not wired: got %#v", a.News)
	}
	if a.Engine != Matcher(eng) {
		t.Errorf("Engine not wired: got %#v", a.Engine)
	}
	if a.Settler != IdeaSettler(settler) {
		t.Errorf("Settler not wired: got %#v", a.Settler)
	}
	if a.Notify != Notifier(ntf) {
		t.Errorf("Notify not wired: got %#v", a.Notify)
	}
}
