package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/thebrchub/aarpaar/config"
)

// APIKeyOnly protects internal endpoints with a shared API key.
// The caller must pass the key in the X-API-Key header.
func APIKeyOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if config.InternalAPIKey == "" {
			http.Error(w, `{"status":"error","message":"Internal API not configured"}`, http.StatusForbidden)
			return
		}
		key := r.Header.Get("X-API-Key")
		if subtle.ConstantTimeCompare([]byte(key), []byte(config.InternalAPIKey)) != 1 {
			http.Error(w, `{"status":"error","message":"Invalid API key"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	}
}
