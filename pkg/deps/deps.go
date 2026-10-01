// Package deps declares, in one place, the subsystem dependencies the Dibs
// entry layers (HTTP server, JSON API, MCP endpoint) are wired with.
//
// It exists so adding a subsystem is a single-file change: pkg/server and
// pkg/mcpserver embed Deps and pkg/api builds from it, instead of each
// restating the dependency list and being hand-copied field by field at
// every construction site (where a missed field is silent, not a compile
// error). It deliberately holds the concrete pointers callers own; pkg/api
// narrows them to its own interface seams on the way in.
package deps

import (
	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/news"
	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

// Deps is the full set of Dibs subsystem dependencies. Nil-able members
// document the degraded mode they switch off.
type Deps struct {
	Store    *store.Store
	Registry *registry.Registry
	History  *history.Store
	News     *news.Store
	// Engine scores idea↔repo matches (nil disables matching).
	Engine *match.Engine
	// Settler opens credited GitHub issues on accept (nil-GitHub records
	// accepts without opening issues).
	Settler *settle.Settler
	// Notify is the in-app notification store (nil disables).
	Notify *notify.Store
}
