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
func JSONSuccess(w http.ResponseWriter, data any) {
	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(data)
}

// JSONMessage writes a simple {"status":"...", "message":"..."} response.
// Use this for one-liner confirmations like "User reported successfully".
func JSONMessage(w http.ResponseWriter, status string, message string) {
	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(statusMessage{
		Status:  status,
		Message: message,
	})
}

// JSONError writes an error response with the given HTTP status code.
// The body is {"status":"error", "message":"..."} so the frontend always
// gets a consistent shape regardless of success or failure.
func JSONError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(statusMessage{
		Status:  "error",
		Message: message,
	})
}
