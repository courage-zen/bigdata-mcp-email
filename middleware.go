package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// bearerAuth validates Authorization: Bearer <key> against a comma-separated
// key set. An empty key set disables auth (local dev). /health is exempt.
func bearerAuth(keys string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, k := range strings.Split(keys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			allowed[k] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(allowed) == 0 || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) || !allowed[strings.TrimSpace(h[len(prefix):])] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "unauthorized",
				"message": "Invalid or missing authentication key",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
