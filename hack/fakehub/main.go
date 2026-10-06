// Command fakehub is a tiny dev-only stand-in for the hive hub, implementing
// the two endpoints Dibs consumes:
//
//	GET /api/saas/whoami        — any non-empty hive_hub_user cookie → the dev user
//	GET /api/saas/dibs/repos  — a small static repo list
//
// Usage:
//
//	go run ./hack/fakehub               # listens on :9999
//	HUB_URL=http://127.0.0.1:9999 DATA_DIR=./data go run ./cmd/dibs
//	curl -H 'Cookie: hive_hub_user=dev' http://127.0.0.1:8080/api/me
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

func main() {
	addr := os.Getenv("FAKEHUB_ADDR")
	if addr == "" {
		addr = ":9999"
	}
	log.Printf("fakehub listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newHandler()))
}

// newHandler builds the fakehub mux. It is split from main so tests can
// drive it through httptest against the real pkg/auth and pkg/registry hub
// clients, keeping fakehub's response shapes in step with what Dibs decodes.
func newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/saas/whoami", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("hive_hub_user")
		if err != nil || c.Value == "" {
			http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"username":     c.Value,
			"display_name": "Dev " + c.Value,
			"email":        c.Value + "@example.com",
			"avatar_url":   "https://github.com/" + c.Value + ".png",
		})
	})
	mux.HandleFunc("GET /api/saas/dibs/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"repoID": "kubestellar/kubestellar", "hiveID": "hive-ks", "owner": "dev", "description": "Multi-cluster configuration management"},
			{"repoID": "kubestellar/dibs", "hiveID": "hive-ks", "owner": "dev", "description": "A marketplace of ideas"},
		})
	})
	return mux
}
