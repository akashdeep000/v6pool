package proxy

import (
	"encoding/json"
	"net/http"
)

// StatsHandler returns the stats HTTP handler serving /stats (JSON) and
// /metrics (Prometheus text format) on the stats listener. Both endpoints are
// gated by the configured token via query parameter or Bearer header.
func (p *Proxy) StatsHandler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		if !tokenOK(token, r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.stats.JSON())
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !tokenOK(token, r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		p.stats.WritePrometheus(w, version, p.PoolMode())
	})
	return mux
}

// tokenOK reports whether the request carries the stats token, either as the
// ?token= query parameter or as an Authorization: Bearer header. An empty
// configured token allows unauthenticated access.
func tokenOK(token string, r *http.Request) bool {
	if token == "" {
		return true
	}
	if ConstantTimeEqual(token, r.URL.Query().Get("token")) {
		return true
	}
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && h[:len(prefix)] == prefix {
		return ConstantTimeEqual(token, h[len(prefix):])
	}
	return false
}

// version is injected at build time via -ldflags.
var version = "dev"
