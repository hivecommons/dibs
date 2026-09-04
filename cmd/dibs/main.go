// Command dibs serves the Dibs marketplace: JSON API + embedded static UI
// in a single process, under DIBS_BASE_PATH (default "/" — Dibs is served
// at its own subdomain, dibs.kubestellar.io).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hivecommons/dibs/pkg/api"
	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/catalog"
	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/news"
	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/server"
	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

// Stamped at build time via -ldflags (see Dockerfile). The freshness probe
// reads `dibs --version`, so keep the output format stable.
var (
	gitHash  = "unknown"
	gitShort = "unknown"
)

const (
	defaultAddr    = ":8080"
	defaultHubURL  = "https://hive.kubestellar.io"
	defaultDataDir = "/data"
	// registrySyncInterval is how often the hub's repo list is re-pulled.
	registrySyncInterval = 5 * time.Minute
	hubSyncTimeout       = 30 * time.Second
)

// envOr reads key, honoring the legacy IDEATE_-prefixed name (the product's
// pre-rename env prefix) as a fallback before the default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if after, ok := strings.CutPrefix(key, "DIBS_"); ok {
		if v := os.Getenv("IDEATE_" + after); v != "" {
			return v
		}
	}
	return def
}

// displayBasePath renders the normalized base path ("" means root) for logs.
func displayBasePath(base string) string {
	if base == "" {
		return "/"
	}
	return base
}

func main() {
	showVersion := flag.Bool("version", false, "print the embedded commit and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("dibs %s (%s)\n", gitShort, gitHash)
		return
	}

	addr := envOr("DIBS_ADDR", defaultAddr)
	basePath := server.NormalizeBasePath(envOr("DIBS_BASE_PATH", server.DefaultBasePath))
	hubURL := envOr("HUB_URL", defaultHubURL)
	dataDir := envOr("DATA_DIR", defaultDataDir)

	st, err := store.New(dataDir)
	if err != nil {
		log.Fatalf("opening idea store: %v", err)
	}
	reg, err := registry.New(dataDir)
	if err != nil {
		log.Fatalf("opening repo registry: %v", err)
	}
	hist, err := history.NewStore(dataDir)
	if err != nil {
		log.Fatalf("opening repo history store: %v", err)
	}
	newsStore, err := news.NewStore(dataDir)
	if err != nil {
		log.Fatalf("opening repo news store: %v", err)
	}
	cncfCatalog, err := catalog.New(dataDir, envOr(settle.EnvGitHubToken, ""))
	if err != nil {
		log.Fatalf("opening CNCF catalog: %v", err)
	}
	cncfCatalog.RefreshAsync()

	notifications, err := notify.New(dataDir)
	if err != nil {
		log.Fatalf("opening notification store: %v", err)
	}

	// Match engine: LLM via hive's litellm gateway when DIBS_LLM_BASE_URL
	// is set, deterministic keyword fallback otherwise — Dibs fully works
	// without a gateway.
	llm := match.LLMFromEnv()
	if llm != nil {
		log.Printf("match engine: llm gateway %s (model %s)", llm.BaseURL, llm.Model)
	} else {
		log.Printf("match engine: %s unset — deterministic fallback matcher", match.EnvLLMBaseURL)
	}
	engine := &match.Engine{Store: st, Registry: reg, Catalog: cncfCatalog, LLM: llm, Notifier: &api.MatchNotifier{Notify: notifications}}

	// Settlement: by default Dibs is just the matchmaker — the ideator
	// files the credited issue via a prefilled GitHub URL. Setting
	// DIBS_GITHUB_TOKEN enables the demoted legacy mode where Dibs opens
	// the issue server-side on accept.
	settler := &settle.Settler{}
	if gh := settle.FromEnv(); gh != nil {
		settler.GitHub = gh
		log.Printf("settlement: %s set — LEGACY server-side issue creation enabled", settle.EnvGitHubToken)
	} else {
		log.Printf("settlement: matchmaker mode — ideators file issues via prefilled GitHub URLs")
	}
	backfiller := history.NewBackfiller(hist, envOr(settle.EnvGitHubToken, ""))
	newsGen := news.NewGenerator(newsStore, backfiller, llm)

	if seed := os.Getenv("REPOS_SEED_FILE"); seed != "" {
		if err := reg.LoadSeedFile(seed); err != nil {
			log.Fatalf("loading REPOS_SEED_FILE: %v", err)
		}
		log.Printf("seeded repo registry from %s", seed)
	}

	// Background hub→registry sync. Failures are logged, never fatal: the
	// registry keeps serving its last-known (or seeded) state.
	hubRepos := &registry.HTTPHubClient{BaseURL: hubURL, Token: envOr(settle.EnvGitHubToken, "")}
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), hubSyncTimeout)
			if err := reg.Sync(ctx, hubRepos); err != nil {
				log.Printf("registry sync: %v", err)
			} else {
				repos := reg.List(false)
				backfiller.RefreshAsync(repos)
				newsGen.RefreshAsync(repos)
			}
			cancel()
			time.Sleep(registrySyncInterval)
		}
	}()

	handler := server.New(server.Config{
		BasePath: basePath,
		HubURL:   hubURL,
		Hub:      &auth.HTTPHubClient{BaseURL: hubURL},
		Store:    st,
		Repos:    reg,
		History:  hist,
		News:     newsStore,
		Engine:   engine,
		Settler:  settler,
		Notify:   notifications,
		Version:  gitHash,
	})

	log.Printf("dibs %s listening on %s (base path %s, hub %s, data %s)", gitShort, addr, displayBasePath(basePath), hubURL, dataDir)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
