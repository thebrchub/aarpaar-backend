package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goccy/go-json"
)

// ---------------------------------------------------------------------------
// Unit Tests — JSON Response Helpers
// ---------------------------------------------------------------------------

func TestJSONSuccess(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]interface{}{"status": "ok", "count": 42}

	JSONSuccess(w, data)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var body map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, float64(42), body["count"])
}

func TestJSONMessage(t *testing.T) {
	w := httptest.NewRecorder()

	JSONMessage(w, "success", "User reported successfully")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var body StatusMessage
	err := json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "success", body.Status)
	assert.Equal(t, "User reported successfully", body.Message)
}

func TestJSONError(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		statusCode int
	}{
		{"bad request", "Invalid input", http.StatusBadRequest},
		{"unauthorized", "Missing auth token", http.StatusUnauthorized},
		{"forbidden", "Access denied", http.StatusForbidden},
		{"not found", "User not found", http.StatusNotFound},
		{"conflict", "DM already exists", http.StatusConflict},
		{"internal error", "Something went wrong", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()

			JSONError(w, tt.message, tt.statusCode)

			assert.Equal(t, tt.statusCode, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			var body StatusMessage
			err := json.Unmarshal(w.Body.Bytes(), &body)
			require.NoError(t, err)
			assert.Equal(t, "error", body.Status)
			assert.Equal(t, tt.message, body.Message)
		})
	}
}

func TestJSONSuccessWithStruct(t *testing.T) {
	type Profile struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	w := httptest.NewRecorder()
	JSONSuccess(w, Profile{Name: "Alice", Email: "alice@test.com"})

	assert.Equal(t, http.StatusOK, w.Code)

	var body Profile
	err := json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "Alice", body.Name)
	assert.Equal(t, "alice@test.com", body.Email)
}

func TestJSONSuccessWithNilData(t *testing.T) {
	w := httptest.NewRecorder()
	JSONSuccess(w, nil)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "null", w.Body.String())
}
