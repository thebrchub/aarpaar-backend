package handlers

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/uuid"
	"github.com/shivanand-burli/go-starter-kit/storage"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/models"
	"github.com/thebrchub/aarpaar/services"
)

// Store is the shared S3/R2 storage client. Initialized in main.go.
var Store storage.StorageService

// PresignUploadHandler generates a presigned POST policy for media upload
// with server-side size enforcement.
// POST /api/v1/arena/media/presign
func PresignUploadHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if Store == nil {
		JSONError(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}

	var req models.PresignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate content type
	ct := strings.ToLower(req.ContentType)
	if !isAllowedMediaType(ct) {
		JSONError(w, "Unsupported media type. Allowed: image/jpeg, image/webp, image/avif, image/png, video/mp4, video/webm", http.StatusBadRequest)
		return
	}

	// Determine max size from admin-configurable arena limits
	limits := services.GetArenaLimits()
	var maxBytes int64
	if isImageType(ct) {
		maxBytes = int64(limits.MaxImageSizeKB) * 1024
	} else {
		maxBytes = int64(limits.MaxVideoSizeKB) * 1024
	}

	// Build object key: arena/{userId}/{uuid}.{ext}
	ext := filepath.Ext(req.Filename)
	if ext == "" {
		ext = mimeToExt(ct)
	}
	objectKey := fmt.Sprintf("arena/%s/%s%s", userID, uuid.New().String(), ext)

	putMins := limits.PresignPutMins
	if putMins <= 0 {
		putMins = config.DefaultPresignPutMins
	}

	out, err := Store.PresignPost(r.Context(), &storage.PresignPostInput{
		Key:         objectKey,
		ContentType: ct,
		MaxBytes:    maxBytes,
		Expiry:      time.Duration(putMins) * time.Minute,
	})
	if err != nil {
		JSONError(w, "Failed to generate upload URL", http.StatusInternalServerError)
		return
	}

	JSONSuccess(w, models.PresignResponse{
		URL:       out.URL,
		Fields:    out.Fields,
		ObjectKey: objectKey,
	})
}

func isAllowedMediaType(ct string) bool {
	switch ct {
	case config.MimeJPEG, config.MimeWebP, config.MimeAVIF, config.MimePNG, config.MimeMp4, config.MimeWebM, config.MimeMOV:
		return true
	}
	return false
}

func isImageType(ct string) bool {
	switch ct {
	case config.MimeJPEG, config.MimeWebP, config.MimeAVIF, config.MimePNG:
		return true
	}
	return false
}

func mimeToExt(ct string) string {
	switch ct {
	case config.MimeJPEG:
		return ".jpg"
	case config.MimeWebP:
		return ".webp"
	case config.MimeAVIF:
		return ".avif"
	case config.MimePNG:
		return ".png"
	case config.MimeMp4:
		return ".mp4"
	case config.MimeWebM:
		return ".webm"
	case config.MimeMOV:
		return ".mov"
	}
	return ""
}
