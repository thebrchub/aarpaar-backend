package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Unit Tests — Arena Helpers
// ---------------------------------------------------------------------------

func TestIsAllowedMediaType(t *testing.T) {
	tests := []struct {
		ct   string
		want bool
	}{
		{"image/jpeg", true},
		{"image/webp", true},
		{"image/avif", true},
		{"image/png", true},
		{"video/mp4", true},
		{"video/webm", true},
		{"video/quicktime", true},
		{"image/gif", false},
		{"video/avi", false},
		{"application/pdf", false},
		{"", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isAllowedMediaType(tt.ct), "ct=%s", tt.ct)
	}
}

func TestIsImageType(t *testing.T) {
	assert.True(t, isImageType("image/jpeg"))
	assert.True(t, isImageType("image/webp"))
	assert.False(t, isImageType("video/mp4"))
	assert.False(t, isImageType("video/webm"))
	assert.False(t, isImageType(""))
}

func TestMimeToExt(t *testing.T) {
	assert.Equal(t, ".jpg", mimeToExt(config.MimeJPEG))
	assert.Equal(t, ".webp", mimeToExt(config.MimeWebP))
	assert.Equal(t, ".mp4", mimeToExt(config.MimeMp4))
	assert.Equal(t, ".webm", mimeToExt(config.MimeWebM))
	assert.Equal(t, ".avif", mimeToExt(config.MimeAVIF))
	assert.Equal(t, ".png", mimeToExt(config.MimePNG))
	assert.Equal(t, ".mov", mimeToExt(config.MimeMOV))
	assert.Equal(t, "", mimeToExt("application/octet-stream"))
}

func TestExtractParentID(t *testing.T) {
	tests := []struct {
		path    string
		wantNil bool
		wantID  int64
	}{
		{"c1", true, 0},                     // top-level, no parent
		{"c5.c12", false, 5},                // reply to c5
		{"c5.c12.c18", false, 12},           // reply to c12
		{"c100.c200.c300.c400", false, 300}, // deep nesting
		{"", true, 0},
	}
	for _, tt := range tests {
		result := extractParentID(tt.path)
		if tt.wantNil {
			assert.Nil(t, result, "path=%s should have nil parent", tt.path)
		} else {
			assert.NotNil(t, result, "path=%s should have parent", tt.path)
			assert.Equal(t, tt.wantID, *result, "path=%s", tt.path)
		}
	}
}

func TestNilIfEmpty(t *testing.T) {
	assert.Nil(t, nilIfEmpty(""))
	assert.Equal(t, "hello", nilIfEmpty("hello"))
}

func TestNilIfZero(t *testing.T) {
	assert.Nil(t, nilIfZero(0))
	assert.Equal(t, 42, nilIfZero(42))
}

func TestParseFeedPagination(t *testing.T) {
	// Uses httptest so we can control query params
	tests := []struct {
		query      string
		wantLimit  int
		wantOffset int
	}{
		{"", config.DefaultFeedLimit, 0},
		{"?limit=10", 10, 0},
		{"?limit=100", config.MaxFeedLimit, 0},
		{"?limit=5&offset=20", 5, 20},
		{"?limit=-1", config.DefaultFeedLimit, 0},
		{"?offset=-5", config.DefaultFeedLimit, 0},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/feed"+tt.query, nil)
		limit, offset := parseFeedPagination(req)
		assert.Equal(t, tt.wantLimit, limit, "query=%s limit", tt.query)
		assert.Equal(t, tt.wantOffset, offset, "query=%s offset", tt.query)
	}
}
