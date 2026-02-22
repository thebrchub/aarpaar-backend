package middleware

import (
	"net/http"

	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// CORS Middleware
//
// Restricts Access-Control-Allow-Origin to the configured CORS_ORIGIN env var.
// Handles preflight OPTIONS requests with a 204 No Content response.
// ---------------------------------------------------------------------------

// CORS wraps an http.Handler and applies CORS headers based on config.CORSOrigin.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Only set CORS headers if the request origin matches the allowed origin
		// or if CORS_ORIGIN is "*" (development mode)
		if origin != "" && (config.CORSOrigin == "*" || origin == config.CORSOrigin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		// Handle preflight requests
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
