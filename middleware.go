package main

import (
	"crypto/subtle"
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
	var keyList []string
	for _, k := range strings.Split(keys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keyList = append(keyList, k)
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
		if !strings.HasPrefix(h, prefix) || !keyMatch(keyList, strings.TrimSpace(h[len(prefix):])) {
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

// keyMatch compares the presented token against every configured key in
// constant time (subtle.ConstantTimeCompare). The previous map lookup was
// timing-sensitive: map hashing short-circuits on the first differing byte,
// leaking prefix matches via response latency. Length is compared first and
// is not secret — only byte equality needs constant-time guarantees.
func keyMatch(keys []string, presented string) bool {
	match := 0
	for _, k := range keys {
		if len(k) != len(presented) {
			continue
		}
		match |= subtle.ConstantTimeCompare([]byte(k), []byte(presented))
	}
	return match == 1
}
