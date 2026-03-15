package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/goccy/go-json"
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
	_ = postgress.GetRawDB().QueryRowContext(ctx, `
		SELECT
			GREATEST((SELECT reltuples FROM pg_class WHERE relname = 'users'), 0),
			(SELECT COALESCE(COUNT(*), 0) FROM users WHERE is_banned = true),
			GREATEST((SELECT reltuples FROM pg_class WHERE relname = 'rooms'), 0),
			(SELECT COALESCE(COUNT(*), 0) FROM rooms WHERE type = 'GROUP'),
			GREATEST((SELECT reltuples FROM pg_class WHERE relname = 'user_reports'), 0),
			(SELECT COALESCE(SUM(amount), 0) FROM donations),
			(SELECT COUNT(DISTINCT user_id) FROM donations)
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

	JSONSuccess(w, stats)
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
		COALESCE(rc.cnt, 0) AS reports_count,
		COALESCE(dt.total, 0) AS total_donated
	 FROM users u
	 LEFT JOIN (SELECT reported_id, COUNT(*) AS cnt FROM user_reports GROUP BY reported_id) rc ON rc.reported_id = u.id
	 LEFT JOIN (SELECT user_id, SUM(amount) AS total FROM donations GROUP BY user_id) dt ON dt.user_id = u.id
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

	rows, err := postgress.GetRawDB().QueryContext(ctx, query, args...)
	if err != nil {
		log.Printf("[admin] users query failed: %v", err)
		JSONError(w, "Database error", http.StatusInternalServerError)
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

	JSONSuccess(w, users)
}

// BanUserHandler bans a user and force-disconnects them.
func BanUserHandler(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("userId")
	if targetID == "" {
		JSONError(w, "userId is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Set is_banned = true
	rows, err := postgress.Exec(`UPDATE users SET is_banned = true WHERE id = $1 AND is_banned = false`, targetID)
	if err != nil {
		log.Printf("[admin] Ban user DB error user=%s: %v", targetID, err)
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		JSONError(w, "User not found or already banned", http.StatusNotFound)
		return
	}

	// Cache ban status in Redis for fast auth middleware checks (24h TTL)
	rdb := redis.GetRawClient()
	rdb.Set(ctx, "ban:"+targetID, "1", 24*time.Hour)

	// Force-disconnect WebSocket
	if e := chat.GetEngine(); e != nil {
		e.DisconnectUser(targetID)
	}

	// Remove from match queue
	rdb.SRem(ctx, config.DefaultMatchQueue, targetID)

	log.Printf("[admin] User %s banned by BENKI_ADMIN", targetID)
	JSONMessage(w, "success", "User banned successfully")
}

// UnbanUserHandler unbans a user.
func UnbanUserHandler(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("userId")
	if targetID == "" {
		JSONError(w, "userId is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	rows, err := postgress.Exec(`UPDATE users SET is_banned = false WHERE id = $1 AND is_banned = true`, targetID)
	if err != nil {
		log.Printf("[admin] Unban user DB error user=%s: %v", targetID, err)
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		JSONError(w, "User not found or not banned", http.StatusNotFound)
		return
	}

	// Remove ban cache
	redis.GetRawClient().Del(ctx, "ban:"+targetID)

	log.Printf("[admin] User %s unbanned by BENKI_ADMIN", targetID)
	JSONMessage(w, "success", "User unbanned successfully")
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
	if err := postgress.GetRawDB().QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		log.Printf("[admin] reports query failed reported_id=%s: %v", reportedID, err)
		JSONError(w, "Database error", http.StatusInternalServerError)
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
		JSONError(w, "userId is required", http.StatusBadRequest)
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
	if err := postgress.GetRawDB().QueryRowContext(ctx, query, targetID).Scan(&raw); err != nil {
		log.Printf("[admin] user reports query failed user=%s: %v", targetID, err)
		JSONError(w, "Database error", http.StatusInternalServerError)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.MinAmount <= 0 {
		JSONError(w, "name and min_amount (> 0) are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	var id int
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`INSERT INTO badge_tiers (name, min_amount, icon, display_order)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		req.Name, req.MinAmount, req.Icon, req.DisplayOrder,
	).Scan(&id)
	if err != nil {
		log.Printf("[admin] create badge tier failed: %v", err)
		JSONError(w, "Failed to create badge tier", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusCreated)
	bytes, _ := json.Marshal(map[string]interface{}{"id": id, "name": req.Name, "min_amount": req.MinAmount})
	w.Write(bytes)
}

// UpdateBadgeTierHandler updates a badge tier.
func UpdateBadgeTierHandler(w http.ResponseWriter, r *http.Request) {
	tierID := r.PathValue("tierId")
	if tierID == "" {
		JSONError(w, "tierId is required", http.StatusBadRequest)
		return
	}

	var req struct {
		Name         *string  `json:"name"`
		MinAmount    *float64 `json:"min_amount"`
		Icon         *string  `json:"icon"`
		DisplayOrder *int     `json:"display_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
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
		JSONError(w, "No fields to update", http.StatusBadRequest)
		return
	}

	query := fmt.Sprintf("UPDATE badge_tiers SET %s, updated_at = NOW() WHERE id = $%d",
		joinStrings(sets, ", "), argIdx)
	args = append(args, tierID)

	rows, err := postgress.Exec(query, args...)
	if err != nil {
		log.Printf("[admin] update badge tier failed id=%s: %v", tierID, err)
		JSONError(w, "Failed to update badge tier", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		JSONError(w, "Badge tier not found", http.StatusNotFound)
		return
	}

	JSONMessage(w, "success", "Badge tier updated")
}

// DeleteBadgeTierHandler deletes a badge tier.
func DeleteBadgeTierHandler(w http.ResponseWriter, r *http.Request) {
	tierID := r.PathValue("tierId")
	if tierID == "" {
		JSONError(w, "tierId is required", http.StatusBadRequest)
		return
	}

	rows, err := postgress.Exec(`DELETE FROM badge_tiers WHERE id = $1`, tierID)
	if err != nil {
		log.Printf("[admin] delete badge tier failed id=%s: %v", tierID, err)
		JSONError(w, "Failed to delete badge tier", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		JSONError(w, "Badge tier not found", http.StatusNotFound)
		return
	}

	JSONMessage(w, "success", "Badge tier deleted")
}

// ---------------------------------------------------------------------------
// BENKI_ADMIN: App Settings
// ---------------------------------------------------------------------------

// GetAppSettingHandler returns a specific app setting.
func GetAppSettingHandler(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		JSONError(w, "key is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	var value []byte
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT value FROM app_settings WHERE key = $1`, key,
	).Scan(&value)
	if err != nil {
		log.Printf("[admin] get setting failed key=%s: %v", key, err)
		JSONError(w, "Setting not found", http.StatusNotFound)
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
		JSONError(w, "key is required", http.StatusBadRequest)
		return
	}

	// Read the raw JSON body as the new value
	var value json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
		JSONError(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	_, cancel := pgCtx(r)
	defer cancel()

	rows, err := postgress.Exec(
		`UPDATE app_settings SET value = $1, updated_at = NOW() WHERE key = $2`,
		value, key,
	)
	if err != nil {
		log.Printf("[admin] update setting failed key=%s: %v", key, err)
		JSONError(w, "Failed to update setting", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		// Insert if not exists
		_, err = postgress.Exec(
			`INSERT INTO app_settings (key, value) VALUES ($1, $2)`, key, value,
		)
		if err != nil {
			log.Printf("[admin] insert setting failed key=%s: %v", key, err)
			JSONError(w, "Failed to create setting", http.StatusInternalServerError)
			return
		}
	}

	JSONMessage(w, "success", "Setting updated")
}

// ---------------------------------------------------------------------------
// BENKI_ADMIN: Bot Toggle
// ---------------------------------------------------------------------------

// GetBotStatusHandler returns the current bot enabled/disabled state.
func GetBotStatusHandler(w http.ResponseWriter, r *http.Request) {
	JSONSuccess(w, map[string]bool{"enabled": services.IsBotEnabled()})
}

// ToggleBotHandler enables or disables bot matchmaking at runtime.
func ToggleBotHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	services.SetBotEnabled(req.Enabled)
	log.Printf("[admin] Bot matching set to %v by BENKI_ADMIN", req.Enabled)
	JSONSuccess(w, map[string]bool{"enabled": services.IsBotEnabled()})
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
	JSONSuccess(w, stats)
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

// GetAppSetting reads a JSONB setting from app_settings and unmarshals it.
func GetAppSetting(ctx context.Context, key string, dest interface{}) error {
	var raw []byte
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT value FROM app_settings WHERE key = $1`, key,
	).Scan(&raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}

// GetUserTotalDonation returns the total donation amount for a user.
func GetUserTotalDonation(ctx context.Context, userID string) float64 {
	var total float64
	postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM donations WHERE user_id = $1`, userID,
	).Scan(&total)
	return total
}

// IsUserVIP checks if a user qualifies as VIP (total donations >= lowest badge tier).
func IsUserVIP(ctx context.Context, userID string) bool {
	var minTier float64
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(MIN(min_amount), 0) FROM badge_tiers`,
	).Scan(&minTier)
	if err != nil || minTier == 0 {
		return false
	}
	return GetUserTotalDonation(ctx, userID) >= minTier
}
