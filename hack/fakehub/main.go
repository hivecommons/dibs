// Command fakehub is a tiny dev-only stand-in for the hive hub, implementing
// the two endpoints Ideate consumes:
//
//	GET /api/saas/whoami        — any non-empty hive_hub_user cookie → the dev user
//	GET /api/saas/ideate/repos  — a small static repo list
//
// Usage:
//
//	go run ./hack/fakehub               # listens on :9999
//	HUB_URL=http://127.0.0.1:9999 DATA_DIR=./data go run ./cmd/ideate
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
	mux.HandleFunc("GET /api/saas/ideate/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"repoID": "kubestellar/kubestellar", "hiveID": "hive-ks", "owner": "dev", "description": "Multi-cluster configuration management"},
			{"repoID": "kubestellar/ideate", "hiveID": "hive-ks", "owner": "dev", "description": "A marketplace of ideas"},
		})
	})
	log.Printf("fakehub listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
