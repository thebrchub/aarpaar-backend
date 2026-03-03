package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// Benchmarks — JSON Response Helpers
// ---------------------------------------------------------------------------

func BenchmarkJSONSuccess(b *testing.B) {
	data := map[string]interface{}{
		"id":       "test-id-123",
		"name":     "Test User",
		"username": "testuser",
		"email":    "test@example.com",
		"active":   true,
	}

	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		JSONSuccess(w, data)
	}
}

func BenchmarkJSONError(b *testing.B) {
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		JSONError(w, "Something went wrong", http.StatusInternalServerError)
	}
}

func BenchmarkJSONMessage(b *testing.B) {
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		JSONMessage(w, "success", "Operation completed successfully")
	}
}
