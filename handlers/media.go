package handlers

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"github.com/shivanand-burli/go-starter-kit/storage"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/helper"
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
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	if Store == nil {
		helper.Error(w, http.StatusServiceUnavailable, "Storage not configured")
		return
	}

	var req models.PresignRequest
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate content type
	ct := strings.ToLower(req.ContentType)
	if !isAllowedMediaType(ct) {
		helper.Error(w, http.StatusBadRequest, "Unsupported media type. Allowed: image/jpeg, image/webp, image/avif, image/png, video/mp4, video/webm")
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
	objectKey := fmt.Sprintf("arena/%s/%s%s", userID, helper.RandomUUID(), ext)

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
		helper.Error(w, http.StatusInternalServerError, "Failed to generate upload URL")
		return
	}

	helper.JSON(w, http.StatusOK, models.PresignResponse{
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
