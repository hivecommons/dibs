// Command ideate serves the Ideate marketplace: JSON API + embedded static UI
// in a single process, under IDEATE_BASE_PATH (default /ideas).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kubestellar/ideate/pkg/auth"
	"github.com/kubestellar/ideate/pkg/registry"
	"github.com/kubestellar/ideate/pkg/server"
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
		Version:  gitHash,
	})

	log.Printf("ideate %s listening on %s (base path %s, hub %s, data %s)", gitShort, addr, basePath, hubURL, dataDir)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
