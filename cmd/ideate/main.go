// Command ideate serves the Ideate marketplace: JSON API + embedded static UI
// in a single process, under IDEATE_BASE_PATH (default "/" — Ideate is served
// at its own subdomain, idea.kubestellar.io).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kubestellar/ideate/pkg/api"
	"github.com/kubestellar/ideate/pkg/auth"
	"github.com/kubestellar/ideate/pkg/match"
	"github.com/kubestellar/ideate/pkg/notify"
	"github.com/kubestellar/ideate/pkg/registry"
	"github.com/kubestellar/ideate/pkg/server"
	"github.com/kubestellar/ideate/pkg/settle"
	"github.com/kubestellar/ideate/pkg/store"
)

// Stamped at build time via -ldflags (see Dockerfile). The freshness probe
// reads `ideate --version`, so keep the output format stable.
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
		fmt.Printf("ideate %s (%s)\n", gitShort, gitHash)
		return
	}

	addr := envOr("IDEATE_ADDR", defaultAddr)
	basePath := server.NormalizeBasePath(envOr("IDEATE_BASE_PATH", server.DefaultBasePath))
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

	notifications, err := notify.New(dataDir)
	if err != nil {
		log.Fatalf("opening notification store: %v", err)
	}

	// Match engine: LLM via hive's litellm gateway when IDEATE_LLM_BASE_URL
	// is set, deterministic keyword fallback otherwise — Ideate fully works
	// without a gateway.
	llm := match.LLMFromEnv()
	if llm != nil {
		log.Printf("match engine: llm gateway %s (model %s)", llm.BaseURL, llm.Model)
	} else {
		log.Printf("match engine: %s unset — deterministic fallback matcher", match.EnvLLMBaseURL)
	}
	engine := &match.Engine{Store: st, Registry: reg, LLM: llm, Notifier: &api.MatchNotifier{Notify: notifications}}

	// Settlement: credited GitHub issues via IDEATE_GITHUB_TOKEN. The hive
	// GitHub App is the tracked follow-up.
	settler := &settle.Settler{}
	if gh := settle.FromEnv(); gh != nil {
		settler.GitHub = gh
		log.Printf("settlement: GitHub token configured")
	} else {
		log.Printf("settlement: %s unset — accepts recorded, issues not opened", settle.EnvGitHubToken)
	}

	if seed := os.Getenv("REPOS_SEED_FILE"); seed != "" {
		if err := reg.LoadSeedFile(seed); err != nil {
			log.Fatalf("loading REPOS_SEED_FILE: %v", err)
		}
		log.Printf("seeded repo registry from %s", seed)
	}

	// Background hub→registry sync. Failures are logged, never fatal: the
	// registry keeps serving its last-known (or seeded) state.
	hubRepos := &registry.HTTPHubClient{BaseURL: hubURL}
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), hubSyncTimeout)
			if err := reg.Sync(ctx, hubRepos); err != nil {
				log.Printf("registry sync: %v", err)
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
		Engine:   engine,
		Settler:  settler,
		Notify:   notifications,
		Version:  gitHash,
	})

	log.Printf("ideate %s listening on %s (base path %s, hub %s, data %s)", gitShort, addr, displayBasePath(basePath), hubURL, dataDir)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
