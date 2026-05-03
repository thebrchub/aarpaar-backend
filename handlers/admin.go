package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/helper"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/services"
)

// ---------------------------------------------------------------------------
// BENKI_ADMIN: Platform Stats
// ---------------------------------------------------------------------------

// GetAdminStatsHandler returns platform-wide statistics.
// Uses pg_class.reltuples for fast approximate row counts instead of full COUNT(*) scans.
func GetAdminStatsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := pgCtx(r)
	defer cancel()

	var totalUsers, bannedUsers, totalRooms, totalGroups, totalReports float64
	var totalDonations, totalDonors float64

	// Fast approximate counts from Postgres stats (updated by autovacuum/ANALYZE)
	_ = postgress.GetPool().QueryRow(ctx, `
		SELECT
			GREATEST((SELECT reltuples FROM pg_class WHERE relname = 'users'), 0),
			GREATEST((SELECT reltuples FROM pg_class WHERE relname = 'users') *
				COALESCE((SELECT avg_fraction FROM (
					SELECT CASE WHEN n_distinct > 0 THEN 1.0/n_distinct
					            WHEN n_distinct < 0 THEN -n_distinct
					            ELSE 0.01 END AS avg_fraction
					FROM pg_stats WHERE tablename = 'users' AND attname = 'is_banned'
					  AND most_common_vals::text LIKE '%true%'
				) sub), 0.01), 0),
			GREATEST((SELECT reltuples FROM pg_class WHERE relname = 'rooms'), 0),
			GREATEST((SELECT reltuples FROM pg_class WHERE relname = 'rooms') * 0.5, 0),
			GREATEST((SELECT reltuples FROM pg_class WHERE relname = 'user_reports'), 0),
			GREATEST((SELECT reltuples FROM pg_class WHERE relname = 'donations'), 0),
			GREATEST((SELECT COALESCE(n_distinct, 0) FROM pg_stats WHERE tablename = 'donations' AND attname = 'user_id'), 0)
	`).Scan(&totalUsers, &bannedUsers, &totalRooms, &totalGroups, &totalReports, &totalDonations, &totalDonors)

	stats := map[string]interface{}{
		"total_users":     totalUsers,
		"banned_users":    bannedUsers,
		"total_rooms":     totalRooms,
		"total_groups":    totalGroups,
		"total_reports":   totalReports,
		"total_donations": totalDonations,
		"total_donors":    totalDonors,
	}

	// Live stats from engine
	if e := chat.GetEngine(); e != nil {
		stats["online_users"] = e.OnlineUserCount()
		stats["active_connections"] = chat.ActiveConnectionCount()
	}

	helper.JSON(w, http.StatusOK, stats)
}

// ---------------------------------------------------------------------------
// BENKI_ADMIN: User Management
// ---------------------------------------------------------------------------

// GetAdminUsersHandler returns a paginated list of users.
func GetAdminUsersHandler(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	banned := r.URL.Query().Get("banned")
	search := r.URL.Query().Get("search")
	sortBy := r.URL.Query().Get("sort")

	query := `SELECT u.id, u.email, u.name, COALESCE(u.username,''), COALESCE(u.avatar_url,''),
		u.gender, u.is_private, u.is_banned, u.created_at, u.last_seen_at,
		u.report_count,
		u.total_donated
	 FROM users u
	 WHERE 1=1`

	args := []interface{}{}
	argIdx := 1

	switch banned {
	case "true":
		query += ` AND u.is_banned = true`
	case "false":
		query += ` AND u.is_banned = false`
	}

	if search != "" {
		query += fmt.Sprintf(` AND (u.name ILIKE '%%' || $%d || '%%' OR u.username ILIKE '%%' || $%d || '%%')`, argIdx, argIdx)
		args = append(args, search)
		argIdx++
	}

	switch sortBy {
	case "reports_count":
		query += ` ORDER BY reports_count DESC, u.created_at DESC`
	default:
		query += ` ORDER BY u.created_at DESC`
	}

	query += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := postgress.GetPool().Query(ctx, query, args...)
	if err != nil {
		log.Printf("[admin] users query failed: %v", err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}
	defer rows.Close()

	type AdminUser struct {
		ID           string  `json:"id"`
		Email        string  `json:"email"`
		Name         string  `json:"name"`
		Username     string  `json:"username"`
		AvatarURL    string  `json:"avatar_url"`
		Gender       string  `json:"gender"`
		IsPrivate    bool    `json:"is_private"`
		IsBanned     bool    `json:"is_banned"`
		CreatedAt    string  `json:"created_at"`
		LastSeenAt   string  `json:"last_seen_at"`
		ReportsCount int     `json:"reports_count"`
		TotalDonated float64 `json:"total_donated"`
	}

	users := []AdminUser{}
	for rows.Next() {
		var u AdminUser
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Username, &u.AvatarURL,
			&u.Gender, &u.IsPrivate, &u.IsBanned, &u.CreatedAt, &u.LastSeenAt,
			&u.ReportsCount, &u.TotalDonated); err != nil {
			log.Printf("[admin] Scan user row failed: %v", err)
			continue
		}
		users = append(users, u)
	}

	helper.JSON(w, http.StatusOK, users)
}

// BanUserHandler bans a user and force-disconnects them.
func BanUserHandler(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("userId")
	if targetID == "" {
		helper.Error(w, http.StatusBadRequest, "userId is required")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Set is_banned = true
	rows, err := postgress.Exec(ctx, `UPDATE users SET is_banned = true WHERE id = $1 AND is_banned = false`, targetID)
	if err != nil {
		log.Printf("[admin] Ban user DB error user=%s: %v", targetID, err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}
	if rows == 0 {
		helper.Error(w, http.StatusNotFound, "User not found or already banned")
		return
	}

	// Cache ban status in Redis for fast auth middleware checks (24h TTL)
	rdb := redis.GetRawClient()
	rdb.Set(ctx, config.CacheBan+targetID, "1", 24*time.Hour)

	// Force-disconnect WebSocket
	if e := chat.GetEngine(); e != nil {
		e.DisconnectUser(targetID)
	}

	// Remove from match queue
	rdb.SRem(ctx, config.DefaultMatchQueue, targetID)

	log.Printf("[admin] User %s banned by BENKI_ADMIN", targetID)
	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "User banned successfully"})
}

// UnbanUserHandler unbans a user.
func UnbanUserHandler(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("userId")
	if targetID == "" {
		helper.Error(w, http.StatusBadRequest, "userId is required")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	rows, err := postgress.Exec(ctx, `UPDATE users SET is_banned = false WHERE id = $1 AND is_banned = true`, targetID)
	if err != nil {
		log.Printf("[admin] Unban user DB error user=%s: %v", targetID, err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}
	if rows == 0 {
		helper.Error(w, http.StatusNotFound, "User not found or not banned")
		return
	}

	// Remove ban cache
	redis.GetRawClient().Del(ctx, config.CacheBan+targetID)

	log.Printf("[admin] User %s unbanned by BENKI_ADMIN", targetID)
	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "User unbanned successfully"})
}

// ---------------------------------------------------------------------------
// BENKI_ADMIN: Reports
// ---------------------------------------------------------------------------

// GetAdminReportsHandler returns a paginated list of user reports.
func GetAdminReportsHandler(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	reportedID := r.URL.Query().Get("reported_id")

	query := `SELECT COALESCE(json_agg(t), '[]')::text FROM (
		SELECT ur.id, ur.reason, ur.created_at,
			ur.reporter_id, rptr.name AS reporter_name, COALESCE(rptr.username,'') AS reporter_username,
			ur.reported_id, rptd.name AS reported_name, COALESCE(rptd.username,'') AS reported_username,
			rptd.is_banned AS reported_is_banned
		FROM user_reports ur
		JOIN users rptr ON rptr.id = ur.reporter_id
		JOIN users rptd ON rptd.id = ur.reported_id`

	args := []interface{}{}
	argIdx := 1

	if reportedID != "" {
		query += fmt.Sprintf(` WHERE ur.reported_id = $%d`, argIdx)
		args = append(args, reportedID)
		argIdx++
	}

	query += fmt.Sprintf(` ORDER BY ur.created_at DESC LIMIT $%d OFFSET $%d) t`, argIdx, argIdx+1)
	args = append(args, limit, offset)

	var raw []byte
	if err := postgress.GetPool().QueryRow(ctx, query, args...).Scan(&raw); err != nil {
		log.Printf("[admin] reports query failed reported_id=%s: %v", reportedID, err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// GetAdminUserReportsHandler returns all reports against a specific user.
func GetAdminUserReportsHandler(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("userId")
	if targetID == "" {
		helper.Error(w, http.StatusBadRequest, "userId is required")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	query := `SELECT COALESCE(json_agg(t), '[]')::text FROM (
		SELECT ur.id, ur.reason, ur.created_at,
			ur.reporter_id, rptr.name AS reporter_name, COALESCE(rptr.username,'') AS reporter_username
		FROM user_reports ur
		JOIN users rptr ON rptr.id = ur.reporter_id
		WHERE ur.reported_id = $1
		ORDER BY ur.created_at DESC
	) t`

	var raw []byte
	if err := postgress.GetPool().QueryRow(ctx, query, targetID).Scan(&raw); err != nil {
		log.Printf("[admin] user reports query failed user=%s: %v", targetID, err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// ---------------------------------------------------------------------------
// BENKI_ADMIN: Badge Tier CRUD
// ---------------------------------------------------------------------------

// CreateBadgeTierHandler creates a new badge tier.
func CreateBadgeTierHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string  `json:"name"`
		MinAmount    float64 `json:"min_amount"`
		Icon         string  `json:"icon"`
		DisplayOrder int     `json:"display_order"`
	}
	if err := helper.ReadJSON(r, &req); err != nil || req.Name == "" || req.MinAmount <= 0 {
		helper.Error(w, http.StatusBadRequest, "name and min_amount (> 0) are required")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	var id int
	err := postgress.GetPool().QueryRow(ctx,
		`INSERT INTO badge_tiers (name, min_amount, icon, display_order)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		req.Name, req.MinAmount, req.Icon, req.DisplayOrder,
	).Scan(&id)
	if err != nil {
		log.Printf("[admin] create badge tier failed: %v", err)
		helper.Error(w, http.StatusInternalServerError, "Failed to create badge tier")
		return
	}

	invalidateBadgeTiersCache()

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusCreated)
	bytes, _ := json.Marshal(map[string]interface{}{"id": id, "name": req.Name, "min_amount": req.MinAmount})
	w.Write(bytes)
}

// UpdateBadgeTierHandler updates a badge tier.
func UpdateBadgeTierHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tierID := r.PathValue("badgeId")
	if tierID == "" {
		helper.Error(w, http.StatusBadRequest, "tierId is required")
		return
	}

	var req struct {
		Name         *string  `json:"name"`
		MinAmount    *float64 `json:"min_amount"`
		Icon         *string  `json:"icon"`
		DisplayOrder *int     `json:"display_order"`
	}
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	_, cancel := pgCtx(r)
	defer cancel()

	// Build dynamic update
	sets := []string{}
	args := []interface{}{}
	argIdx := 1

	if req.Name != nil {
		sets = append(sets, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
	}
	if req.MinAmount != nil {
		sets = append(sets, fmt.Sprintf("min_amount = $%d", argIdx))
		args = append(args, *req.MinAmount)
		argIdx++
	}
	if req.Icon != nil {
		sets = append(sets, fmt.Sprintf("icon = $%d", argIdx))
		args = append(args, *req.Icon)
		argIdx++
	}
	if req.DisplayOrder != nil {
		sets = append(sets, fmt.Sprintf("display_order = $%d", argIdx))
		args = append(args, *req.DisplayOrder)
		argIdx++
	}

	if len(sets) == 0 {
		helper.Error(w, http.StatusBadRequest, "No fields to update")
		return
	}

	query := fmt.Sprintf("UPDATE badge_tiers SET %s, updated_at = NOW() WHERE id = $%d",
		joinStrings(sets, ", "), argIdx)
	args = append(args, tierID)

	rows, err := postgress.Exec(ctx, query, args...)
	if err != nil {
		log.Printf("[admin] update badge tier failed id=%s: %v", tierID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to update badge tier")
		return
	}
	if rows == 0 {
		helper.Error(w, http.StatusNotFound, "Badge tier not found")
		return
	}

	invalidateBadgeTiersCache()

	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "Badge tier updated"})
}

// DeleteBadgeTierHandler deletes a badge tier.
func DeleteBadgeTierHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tierID := r.PathValue("badgeId")
	if tierID == "" {
		helper.Error(w, http.StatusBadRequest, "tierId is required")
		return
	}

	rows, err := postgress.Exec(ctx, `DELETE FROM badge_tiers WHERE id = $1`, tierID)
	if err != nil {
		log.Printf("[admin] delete badge tier failed id=%s: %v", tierID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to delete badge tier")
		return
	}
	if rows == 0 {
		helper.Error(w, http.StatusNotFound, "Badge tier not found")
		return
	}

	invalidateBadgeTiersCache()

	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "Badge tier deleted"})
}

// invalidateBadgeTiersCache removes the cached badge tiers from Redis.
func invalidateBadgeTiersCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	redis.GetRawClient().Del(ctx, "badge_tiers:all")
}

// ---------------------------------------------------------------------------
// BENKI_ADMIN: App Settings
// ---------------------------------------------------------------------------

// GetAppSettingHandler returns a specific app setting.
func GetAppSettingHandler(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		helper.Error(w, http.StatusBadRequest, "key is required")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	var value []byte
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT value FROM app_settings WHERE key = $1`, key,
	).Scan(&value)
	if err != nil {
		log.Printf("[admin] get setting failed key=%s: %v", key, err)
		helper.Error(w, http.StatusNotFound, "Setting not found")
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(value)
}

// UpdateAppSettingHandler updates a specific app setting.
func UpdateAppSettingHandler(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		helper.Error(w, http.StatusBadRequest, "key is required")
		return
	}

	// Read the raw JSON body as the new value
	var value json.RawMessage
	if err := helper.ReadJSON(r, &value); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	rows, err := postgress.Exec(ctx,
		`UPDATE app_settings SET value = $1, updated_at = NOW() WHERE key = $2`,
		value, key,
	)
	if err != nil {
		log.Printf("[admin] update setting failed key=%s: %v", key, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to update setting")
		return
	}
	if rows == 0 {
		// Insert if not exists
		_, err = postgress.Exec(ctx,
			`INSERT INTO app_settings (key, value) VALUES ($1, $2)`, key, value,
		)
		if err != nil {
			log.Printf("[admin] insert setting failed key=%s: %v", key, err)
			helper.Error(w, http.StatusInternalServerError, "Failed to create setting")
			return
		}
	}

	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "Setting updated"})
}

// ---------------------------------------------------------------------------
// BENKI_ADMIN: Bot Toggle
// ---------------------------------------------------------------------------

// GetBotStatusHandler returns the current bot enabled/disabled state.
func GetBotStatusHandler(w http.ResponseWriter, r *http.Request) {
	helper.JSON(w, http.StatusOK, map[string]bool{"enabled": services.IsBotEnabled()})
}

// ToggleBotHandler enables or disables bot matchmaking at runtime.
func ToggleBotHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	services.SetBotEnabled(req.Enabled)
	log.Printf("[admin] Bot matching set to %v by BENKI_ADMIN", req.Enabled)
	helper.JSON(w, http.StatusOK, map[string]bool{"enabled": services.IsBotEnabled()})
}

// ---------------------------------------------------------------------------
// Online Count (Public API)
// ---------------------------------------------------------------------------

// GetOnlineCountHandler returns the current number of online users.
func GetOnlineCountHandler(w http.ResponseWriter, r *http.Request) {
	stats := map[string]interface{}{
		"online_users": 0,
	}
	if e := chat.GetEngine(); e != nil {
		stats["online_users"] = e.OnlineUserCount()
	}
	helper.JSON(w, http.StatusOK, stats)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// parsePagination extracts limit and offset from query params with defaults.
func parsePagination(r *http.Request) (limit, offset int) {
	limit = config.DefaultPageLimit
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		if l > config.MaxPageLimit {
			l = config.MaxPageLimit
		}
		limit = l
	}

	offset = 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o > 0 {
		offset = o
	}

	return
}

// joinStrings joins a string slice with a separator.
func joinStrings(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}

// GetAppSetting reads a setting from the in-memory cache (refreshed every 60s).
// Falls back to Postgres if not cached.
func GetAppSetting(ctx context.Context, key string, dest interface{}) error {
	if services.GetCachedAppSetting(key, dest) {
		return nil
	}
	// Fallback to Postgres (first boot before cache is warm)
	var raw []byte
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT value FROM app_settings WHERE key = $1`, key,
	).Scan(&raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}

// GetUserTotalDonation returns the total donation amount for a user.
// Uses the materialized total_donated column (maintained by trigger).
// Cached in Redis for 2 minutes via redis.Fetch (singleflight dedup).
func GetUserTotalDonation(ctx context.Context, userID string) float64 {
	cacheKey := config.CacheUserDonated + userID
	total, _ := redis.Fetch(ctx, cacheKey, config.CacheTTLLong, func(ctx context.Context) (float64, error) {
		var t float64
		postgress.GetPool().QueryRow(ctx,
			`SELECT total_donated FROM users WHERE id = $1`, userID,
		).Scan(&t)
		return t, nil
	})
	return total
}

// IsUserVIP checks if a user qualifies as VIP (total donations >= lowest badge tier).
// Uses the in-memory cached min tier (refreshed every 60s) — zero PG queries.
func IsUserVIP(ctx context.Context, userID string) bool {
	minTier := services.GetVIPMinTier()
	if minTier == 0 {
		return false
	}
	return GetUserTotalDonation(ctx, userID) >= minTier
}
