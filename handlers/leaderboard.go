package handlers

import (
	"fmt"
	"log"
	"net/http"

	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Leaderboard
// ---------------------------------------------------------------------------

// GetLeaderboardHandler returns top donors.
func GetLeaderboardHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope != "monthly" {
		scope = "alltime"
	}

	limit, offset := parsePagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	var dateFilter string
	if scope == "monthly" {
		// Read leaderboard_config.monthly_reset_day from app_settings (default 1 = first of month)
		var cfg struct {
			MonthlyResetDay int `json:"monthly_reset_day"`
		}
		cfg.MonthlyResetDay = 1 // default
		_ = GetAppSetting(ctx, "leaderboard_config", &cfg)

		// Filter donations from the current period (reset_day of this month to now)
		dateFilter = fmt.Sprintf(
			`AND d.created_at >= (date_trunc('month', NOW()) + interval '%d days' - interval '1 day')`,
			cfg.MonthlyResetDay,
		)
	}

	query := fmt.Sprintf(`SELECT COALESCE(json_agg(t), '[]')::text FROM (
		SELECT
			d.user_id,
			u.name,
			u.avatar_url,
			SUM(d.amount) AS total_donated,
			COUNT(d.id) AS donation_count
		FROM donations d
		JOIN users u ON u.id = d.user_id
		WHERE 1=1 %s
		GROUP BY d.user_id, u.name, u.avatar_url
		ORDER BY total_donated DESC
		LIMIT $1 OFFSET $2
	) t`, dateFilter)

	var raw []byte
	if err := postgress.GetRawDB().QueryRowContext(ctx, query, limit, offset).Scan(&raw); err != nil {
		log.Printf("[leaderboard] query failed scope=%s: %v", scope, err)
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}
