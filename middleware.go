package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// bearerAuth validates Authorization: Bearer <key> against a comma-separated
// key set. Only the exactly-empty key string disables auth (local dev).
// A non-empty string that yields no usable keys (whitespace-only, commas
// only) fails closed: every request 401s instead of silently disabling auth.
// /health is exempt.
func bearerAuth(keys string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, k := range strings.Split(keys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			allowed[k] = true
		}
	}
	authDisabled := keys == ""
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authDisabled || r.URL.Path == "/health" {
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
