package server

import (
	"net/http"

	"github.com/hivecommons/dibs/pkg/match"
)

// MetricsHandler serves the cumulative, bounded counters in Prometheus text
// exposition format at /metrics. It is meant for a separate internal
// listener (DIBS_METRICS_ADDR), not the public mux, so ingress never
// reaches it.
func MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		reqStats.writeProm(w)
		jobStats.writeProm(w)
		match.WritePrometheus(w)
	})
	return mux
}
