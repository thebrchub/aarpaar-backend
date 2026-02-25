package handlers

import (
	"net/http"

	"github.com/goccy/go-json"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Centralized JSON Response Helpers
//
// Every handler uses these instead of manually setting headers and writing
// bytes. This keeps HTTP responses consistent across the entire API.
// ---------------------------------------------------------------------------

// statusMessage is the standard shape for simple API responses.
type statusMessage struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// JSONSuccess writes a 200 OK response with a JSON-encoded body.
// Pass any struct or map — it will be marshalled automatically.
// Uses json.Marshal + w.Write to avoid the trailing newline and per-call
// encoder allocation from json.NewEncoder (P3-1 fix).
func JSONSuccess(w http.ResponseWriter, data any) {
	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	bytes, _ := json.Marshal(data)
	w.Write(bytes)
}

// JSONMessage writes a simple {"status":"...", "message":"..."} response.
// Use this for one-liner confirmations like "User reported successfully".
func JSONMessage(w http.ResponseWriter, status string, message string) {
	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	bytes, _ := json.Marshal(statusMessage{
		Status:  status,
		Message: message,
	})
	w.Write(bytes)
}

// JSONError writes an error response with the given HTTP status code.
// The body is {"status":"error", "message":"..."} so the frontend always
// gets a consistent shape regardless of success or failure.
func JSONError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(statusCode)
	bytes, _ := json.Marshal(statusMessage{
		Status:  "error",
		Message: message,
	})
	w.Write(bytes)
}
