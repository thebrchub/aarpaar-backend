package handlers

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/goccy/go-json"

	"github.com/google/uuid"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/services"
)

// ---------------------------------------------------------------------------
// Matchmaking: Enter Queue
// ---------------------------------------------------------------------------

// EnterMatchQueueHandler tries to find an instant stranger match.
// If no match is found, the user is placed in a Redis queue to wait.
//
// @Summary		Enter matchmaking queue
// @Description	Attempts instant stranger match. If no match, queues the user and schedules a bot fallback.
// @Tags		Matchmaking
// @Produce		json
// @Success		200	{object}	MatchResponse		"Matched instantly or queued"
// @Failure		401	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/match/enter [post]
func EnterMatchQueueHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse optional location from request body (geolocation passthrough)
	var body struct {
		Location *json.RawMessage `json:"location,omitempty"` // opaque JSON object from frontend
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // best-effort; body may be empty

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	rdb := redis.GetRawClient()

	// Store location in Redis if provided (5-min TTL, auto-expires)
	if body.Location != nil && len(*body.Location) > 2 { // not "{}"
		rdb.Set(ctx, config.MATCH_LOCATION_COLON+userID, []byte(*body.Location), 5*time.Minute)
	}

	// Single queue for all users (no gender preference)
	queue := config.DefaultMatchQueue

	// Idempotency guard: if the user is already in the queue, don't process again
	alreadyQueued, _ := rdb.SIsMember(ctx, queue, userID).Result()
	if alreadyQueued {
		JSONMessage(w, "already_queued", "You are already in the queue")
		return
	}

	// Try to find a partner who hasn't blocked us (and vice versa)
	var matchedPartner string

	for range config.MaxMatchAttempts {
		// Pull a random person from the queue
		partnerID, err := rdb.SPop(ctx, queue).Result()
		if err != nil || partnerID == "" {
			break // Queue is empty
		}

		// Skip if we somehow pulled ourselves
		if partnerID == userID {
			continue
		}

		// Check Postgres: did either of us block the other?
		query := `
			SELECT EXISTS (
				SELECT 1 FROM blocked_users 
				WHERE (blocker_id = $1 AND blocked_id = $2) 
				   OR (blocker_id = $2 AND blocked_id = $1)
			)
		`
		var isBlocked bool
		err = postgress.GetRawDB().QueryRow(query, userID, partnerID).Scan(&isBlocked)

		if err == nil && !isBlocked {
			// Check if we're already friends — friends shouldn't be matched as strangers
			uid1, uid2 := sortUUIDs(userID, partnerID)
			var areFriends bool
			postgress.GetRawDB().QueryRow(
				`SELECT EXISTS (SELECT 1 FROM friendships WHERE user_id_1 = $1 AND user_id_2 = $2)`,
				uid1, uid2,
			).Scan(&areFriends)

			if !areFriends {
				// Found a clean match
				matchedPartner = partnerID
				break
			}
		}

		// Blocked or already friends — put them back for someone else
		rdb.SAdd(ctx, queue, partnerID)
	}

	// Route the result
	if matchedPartner != "" {
		// Create a unique stranger room and notify both users
		roomID := config.STRANGER_PREFIX + uuid.New().String()

		// Store both participants in Redis so MatchActionHandler can resolve
		// the partner server-side without requiring partner_username from the client
		rdb.SAdd(ctx, config.STRANGER_MEMBERS_COLON+roomID, userID, matchedPartner)
		rdb.Expire(ctx, config.STRANGER_MEMBERS_COLON+roomID, 24*time.Hour)

		// Fetch partner locations for geolocation passthrough
		userLocRaw, _ := rdb.Get(ctx, config.MATCH_LOCATION_COLON+userID).Result()
		partnerLocRaw, _ := rdb.Get(ctx, config.MATCH_LOCATION_COLON+matchedPartner).Result()

		notifyMatchWithLocation(ctx, userID, roomID, partnerLocRaw)
		notifyMatchWithLocation(ctx, matchedPartner, roomID, userLocRaw)

		// Clean up location keys after match
		rdb.Del(ctx, config.MATCH_LOCATION_COLON+userID, config.MATCH_LOCATION_COLON+matchedPartner)

		// Cancel any pending bot timers for both users
		services.CancelBotMatch(userID)
		services.CancelBotMatch(matchedPartner)

		// Auto-subscribe both users to the stranger room (no join_room needed)
		if e := chat.GetEngine(); e != nil {
			e.JoinRoomForUser(userID, roomID)
			e.JoinRoomForUser(matchedPartner, roomID)
		}

		JSONSuccess(w, map[string]string{
			"status":  "matched",
			"message": "Match found instantly",
			"room_id": roomID,
		})
		return
	}

	// No match — add ourselves to the queue and wait for someone else
	rdb.SAdd(ctx, queue, userID)

	// Schedule a bot match fallback after BotMatchDelay
	services.ScheduleBotMatch(userID)

	JSONMessage(w, "queued", "Waiting for a match...")
}

// notifyMatch sends a "match_found" event to a specific user via Redis Pub/Sub.
// The engine picks this up and delivers it over their WebSocket.
func notifyMatch(ctx context.Context, targetUser, roomID string) {
	notifyMatchWithLocation(ctx, targetUser, roomID, "")
}

// notifyMatchWithLocation sends a "match_found" event including optional partner location.
func notifyMatchWithLocation(ctx context.Context, targetUser, roomID, partnerLocation string) {
	eventPayload := map[string]interface{}{
		config.FieldType:            config.MsgTypeMatchFound,
		config.FieldRoomID:          roomID,
		config.FieldPartnerFakeName: services.PickRandomName(),
		config.FieldPartnerAvatar:   "",
	}

	// If partner shared their location, include it as opaque JSON
	if partnerLocation != "" {
		eventPayload["partner_location"] = json.RawMessage(partnerLocation)
	}

	// Wrap in the routing envelope that engine.go expects
	envelope := map[string]interface{}{
		config.FieldType: config.MsgTypePrivate,
		config.FieldTo:   targetUser,
		config.FieldFrom: config.SystemSender,
		config.FieldData: eventPayload,
	}

	envBytes, err := json.Marshal(envelope)
	if err != nil {
		log.Printf("[match] Failed to marshal match notification: %v", err)
		return
	}
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, envBytes)
}

// ---------------------------------------------------------------------------
// Matchmaking: Leave Queue
// ---------------------------------------------------------------------------

// LeaveMatchQueueHandler removes the user from the matchmaking queue.
//
// @Summary		Leave matchmaking queue
// @Description	Removes the user from the matchmaking queue and cancels any pending bot match.
// @Tags		Matchmaking
// @Produce		json
// @Success		200	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/match/leave [post]
func LeaveMatchQueueHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Remove from the single matchmaking queue
	ctx, cancel := context.WithTimeout(r.Context(), config.RedisOpTimeout)
	defer cancel()
	rdb := redis.GetRawClient()
	rdb.SRem(ctx, config.DefaultMatchQueue, userID)

	// Clean up stored location
	rdb.Del(ctx, config.MATCH_LOCATION_COLON+userID)

	// Cancel any pending bot match timer
	services.CancelBotMatch(userID)

	JSONMessage(w, "success", "Removed from queue")
}

// ---------------------------------------------------------------------------
// Match Actions: Skip / Block / Disconnect
// ---------------------------------------------------------------------------

type MatchActionRequest struct {
	PartnerUsername string `json:"partner_username"`
	RoomID          string `json:"room_id"`
	Action          string `json:"action"` // "skip" or "block"
}

// MatchActionHandler processes skip/block/friend actions during a stranger chat.
//
// @Summary		Perform match action
// @Description	Processes skip, block, or friend actions during a stranger chat session.
// @Tags		Matchmaking
// @Accept		json
// @Produce		json
// @Param		body	body	MatchActionRequest	true	"Action details"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/match/action [post]
func MatchActionHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req MatchActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request", http.StatusBadRequest)
		return
	}

	ctx, ctxCancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer ctxCancel()
	rdb := redis.GetRawClient()

	// Resolve partner: prefer server-side lookup from Redis stranger_members set,
	// fall back to partner_username if provided (backward compatible)
	var partnerID string
	if req.RoomID != "" {
		members, _ := rdb.SMembers(ctx, config.STRANGER_MEMBERS_COLON+req.RoomID).Result()
		for _, m := range members {
			if m != userID {
				partnerID = m
				break
			}
		}
	}
	// Fallback: resolve from partner_username if Redis lookup didn't find it
	if partnerID == "" && req.PartnerUsername != "" {
		err := postgress.GetRawDB().QueryRow(
			`SELECT id FROM users WHERE username = $1`, req.PartnerUsername,
		).Scan(&partnerID)
		if err != nil {
			log.Printf("[match] Failed to resolve partner username %s: %v", req.PartnerUsername, err)
		}
	}

	// ---- Bot partner detection ----
	isBot := services.IsBotUser(partnerID)

	// ---- Friend request (mutual opt-in) ----
	if req.Action == config.ActionFriend {
		if partnerID == "" || req.RoomID == "" {
			JSONError(w, "room_id is required", http.StatusBadRequest)
			return
		}

		// Bot cannot be friended — auto-reject (bot "disconnects")
		if isBot {
			services.StopBotSession(req.RoomID)
			notifyUser(ctx, userID, map[string]interface{}{
				config.FieldType:   config.MsgTypeStrangerDisconnected,
				config.FieldRoomID: req.RoomID,
			})
			rdb.Set(ctx, config.CHAT_CLOSED_COLON+req.RoomID, "1", 24*time.Hour)
			rdb.Del(ctx, config.STRANGER_MEMBERS_COLON+req.RoomID)
			closedEvent, _ := json.Marshal(map[string]string{
				config.FieldType:   config.MsgTypeRoomClosed,
				config.FieldRoomID: req.RoomID,
			})
			redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, closedEvent)
			JSONMessage(w, "disconnected", "Stranger has left the chat")
			return
		}

		myKey := config.FRIEND_REQUEST_COLON + req.RoomID + ":" + userID
		partnerKey := config.FRIEND_REQUEST_COLON + req.RoomID + ":" + partnerID

		// Record in Redis for fast mutual detection (1h TTL)
		rdb.Set(ctx, myKey, "1", 1*time.Hour)

		// Also persist to Postgres so it survives logout/restart
		postgress.Exec(
			`INSERT INTO friend_requests (sender_id, receiver_id, status, stranger_room_id)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (sender_id, receiver_id) DO UPDATE SET status = $3, stranger_room_id = $4, updated_at = NOW()`,
			userID, partnerID, config.FriendReqPending, req.RoomID,
		)

		// Check if the partner already sent a friend request (Redis fast path)
		exists, _ := rdb.Exists(ctx, partnerKey).Result()

		// If not in Redis, also check Postgres (they may have logged out)
		if exists == 0 {
			var pgExists bool
			postgress.GetRawDB().QueryRow(
				`SELECT EXISTS (SELECT 1 FROM friend_requests
				 WHERE sender_id = $1 AND receiver_id = $2 AND status = $3)`,
				partnerID, userID, config.FriendReqPending,
			).Scan(&pgExists)
			if pgExists {
				exists = 1
			}
		}

		if exists == 0 {
			// One-sided: notify partner that we want to be friends
			notifyUser(ctx, partnerID, map[string]any{
				config.FieldType:   config.MsgTypeFriendRequest,
				config.FieldRoomID: req.RoomID,
				config.FieldFrom:   userID,
			})
			JSONMessage(w, "pending", "Friend request sent, waiting for partner")
			return
		}

		// Mutual! Clean up Redis keys
		rdb.Del(ctx, myKey, partnerKey)

		// Update both friend_requests to accepted
		postgress.Exec(
			`UPDATE friend_requests SET status = $1, updated_at = NOW()
			 WHERE (sender_id = $2 AND receiver_id = $3) OR (sender_id = $3 AND receiver_id = $2)`,
			config.FriendReqAccepted, userID, partnerID,
		)

		// Create friendship
		uid1, uid2 := sortUUIDs(userID, partnerID)
		postgress.Exec(
			`INSERT INTO friendships (user_id_1, user_id_2) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			uid1, uid2,
		)

		// Create a permanent DM room in Postgres
		tx, err := postgress.GetRawDB().Begin()
		if err != nil {
			JSONError(w, "Database error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		var dmRoomID string
		err = tx.QueryRow(`INSERT INTO rooms (type) VALUES ('DM') RETURNING id`).Scan(&dmRoomID)
		if err != nil {
			JSONError(w, "Failed to create room", http.StatusInternalServerError)
			return
		}

		_, err = tx.Exec(
			`INSERT INTO room_members (room_id, user_id) VALUES ($1, $2), ($1, $3)`,
			dmRoomID, userID, partnerID,
		)
		if err != nil {
			JSONError(w, "Failed to add members", http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(); err != nil {
			JSONError(w, "Failed to commit", http.StatusInternalServerError)
			return
		}
		// Auto-subscribe both users to the new permanent DM room
		if e := chat.GetEngine(); e != nil {
			e.JoinRoomForUser(userID, dmRoomID)
			e.JoinRoomForUser(partnerID, dmRoomID)
		}
		// Notify both users about the new DM room
		acceptPayload := map[string]interface{}{
			config.FieldType:   config.MsgTypeFriendAccepted,
			config.FieldRoomID: req.RoomID,
			"dm_room_id":       dmRoomID,
		}
		notifyUser(ctx, userID, acceptPayload)
		notifyUser(ctx, partnerID, acceptPayload)

		// Close the stranger room — they now have a permanent DM
		rdb.Set(ctx, config.CHAT_CLOSED_COLON+req.RoomID, "1", 24*time.Hour)
		rdb.Del(ctx, config.STRANGER_MEMBERS_COLON+req.RoomID) // clean up members set
		closedEvent, _ := json.Marshal(map[string]string{
			config.FieldType:   config.MsgTypeRoomClosed,
			config.FieldRoomID: req.RoomID,
		})
		redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, closedEvent)

		JSONSuccess(w, map[string]string{
			"status":     "friends",
			"message":    "You are now friends!",
			"dm_room_id": dmRoomID,
		})
		return
	}

	// ---- Skip / Block ----

	// Stop bot session if partner is a bot
	if isBot && req.RoomID != "" {
		services.StopBotSession(req.RoomID)
	}

	// 1. If the action is "block", save it to Postgres (no-op for bots — ON CONFLICT DO NOTHING)
	if req.Action == config.ActionBlock && partnerID != "" && !isBot {
		query := `
			INSERT INTO blocked_users (blocker_id, blocked_id) 
			VALUES ($1, $2) ON CONFLICT DO NOTHING;
		`
		postgress.Exec(query, userID, partnerID)
	}

	// 2. Notify the partner that the stranger disconnected (skip for bots)
	if partnerID != "" && req.RoomID != "" && !isBot {
		notifyUser(ctx, partnerID, map[string]interface{}{
			config.FieldType:   config.MsgTypeStrangerDisconnected,
			config.FieldRoomID: req.RoomID,
		})
	}

	// 3. Mark the stranger room as closed in Redis (24h TTL)
	if req.RoomID != "" {
		rdb.Set(ctx, config.CHAT_CLOSED_COLON+req.RoomID, "1", 24*time.Hour)
		rdb.Del(ctx, config.STRANGER_MEMBERS_COLON+req.RoomID) // clean up members set

		// Broadcast room_closed to all servers so every connected client gets kicked
		closedEvent, _ := json.Marshal(map[string]string{
			config.FieldType:   config.MsgTypeRoomClosed,
			config.FieldRoomID: req.RoomID,
		})
		redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, closedEvent)
	}

	// 4. Clean up any pending friend requests for this room
	if req.RoomID != "" {
		rdb.Del(ctx,
			config.FRIEND_REQUEST_COLON+req.RoomID+":"+userID,
			config.FRIEND_REQUEST_COLON+req.RoomID+":"+partnerID,
		)
		// Also clean up Postgres friend requests for this stranger room
		if partnerID != "" {
			postgress.Exec(
				`DELETE FROM friend_requests WHERE stranger_room_id = $1
				 AND ((sender_id = $2 AND receiver_id = $3) OR (sender_id = $3 AND receiver_id = $2))`,
				req.RoomID, userID, partnerID,
			)
		}
	}

	JSONMessage(w, "success", "Action processed")
}

// notifyUser sends a private system event to a user via Redis Pub/Sub.
func notifyUser(ctx context.Context, targetUserID string, data map[string]interface{}) {
	envelope := map[string]interface{}{
		config.FieldType: config.MsgTypePrivate,
		config.FieldTo:   targetUserID,
		config.FieldFrom: config.SystemSender,
		config.FieldData: data,
	}
	envBytes, err := json.Marshal(envelope)
	if err != nil {
		log.Printf("[match] Failed to marshal notification: %v", err)
		return
	}
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, envBytes)
}

// ---------------------------------------------------------------------------
// Report User
// ---------------------------------------------------------------------------

type ReportRequest struct {
	ReportedUsername string `json:"reported_username"`
	Reason           string `json:"reason"`
}

// ReportUserHandler saves a user report to Postgres for moderation review.
//
// @Summary		Report a user
// @Description	Saves a user report for moderation review. Auto-blocks the reported user.
// @Tags		Matchmaking
// @Accept		json
// @Produce		json
// @Param		body	body	ReportRequest	true	"Report details"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/match/report [post]
func ReportUserHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req ReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ReportedUsername == "" || req.Reason == "" {
		JSONError(w, "Invalid request or missing fields", http.StatusBadRequest)
		return
	}

	// Resolve username to UUID
	var reportedID string
	err := postgress.GetRawDB().QueryRow(
		`SELECT id FROM users WHERE username = $1`, req.ReportedUsername,
	).Scan(&reportedID)
	if err != nil || reportedID == "" {
		JSONError(w, "Reported user not found", http.StatusNotFound)
		return
	}

	query := `INSERT INTO user_reports (reporter_id, reported_id, reason) VALUES ($1, $2, $3)`
	_, err = postgress.Exec(query, userID, reportedID, req.Reason)
	if err != nil {
		JSONError(w, "Failed to submit report", http.StatusInternalServerError)
		return
	}

	JSONMessage(w, "success", "User reported successfully")
}
