package middleware

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// BenkiAdminOnly Middleware
//
// Checks that the authenticated user's email matches the BENKI_ADMIN_EMAIL
// env var. Must be chained AFTER the Auth middleware (needs UserIDKey in ctx).
//
// Usage: mux.HandleFunc("GET /api/v1/admin/stats", mw.Auth(mw.BenkiAdminOnly(handler)))
// ---------------------------------------------------------------------------

// BenkiAdminOnly restricts access to the configured BENKI_ADMIN_EMAIL.
// Returns 403 Forbidden if the user is not the super admin.
func BenkiAdminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If no admin email is configured, reject all admin requests
		if config.BenkiAdminEmail == "" {
			http.Error(w, `{"status":"error","message":"Admin access not configured"}`, http.StatusForbidden)
			return
		}

		userID, ok := r.Context().Value(config.UserIDKey).(string)
		if !ok || userID == "" {
			http.Error(w, `{"status":"error","message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// Look up the user's email from Postgres
		var email string
		adminCtx, adminCancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer adminCancel()
		err := postgress.GetRawDB().QueryRowContext(adminCtx,
			`SELECT email FROM users WHERE id = $1`, userID,
		).Scan(&email)
		if err != nil || email == "" {
			log.Printf("[admin] Email lookup failed user=%s: %v", userID, err)
			http.Error(w, `{"status":"error","message":"User not found"}`, http.StatusForbidden)
			return
		}

		if email != config.BenkiAdminEmail {
			http.Error(w, `{"status":"error","message":"Forbidden: admin access required"}`, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	}
}
