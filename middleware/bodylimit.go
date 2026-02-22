package middleware

import (
	"net/http"

	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Request Body Size Limiter
//
// Wraps r.Body with http.MaxBytesReader to prevent oversized payloads.
// If a client sends more than MaxRequestBodySize (1 MB), the server
// returns 413 Request Entity Too Large automatically when the handler
// tries to read the body.
// ---------------------------------------------------------------------------

// BodyLimit wraps an http.Handler and limits the request body size.
func BodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, config.MaxRequestBodySize)
		next.ServeHTTP(w, r)
	})
}
