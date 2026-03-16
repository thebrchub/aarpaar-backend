package services

import (
	"log"

	"github.com/shivanand-burli/go-starter-kit/postgress"
)

// ---------------------------------------------------------------------------
// Auto-Migration
//
// RunMigrations creates all required database objects (tables, indexes,
// triggers, extensions) if they don't already exist. This is safe to run
// on every startup — every statement uses IF NOT EXISTS / OR REPLACE.
//
// Call this once during application boot, after Postgres is connected.
// ---------------------------------------------------------------------------

// migrationStatements is the ordered list of DDL to execute on startup.
// Each statement is idempotent (safe to re-run).
var migrationStatements = []string{

	// ===================================================================
	// EXTENSIONS
	// ===================================================================

	`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,

	`CREATE EXTENSION IF NOT EXISTS "pg_trgm"`,

	// ===================================================================
	// TABLE: users
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS users (
        id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
        google_id   TEXT NOT NULL,
        email       TEXT NOT NULL,
        name        TEXT NOT NULL,
        username    VARCHAR(30),
        avatar_url  TEXT DEFAULT '',
        mobile      TEXT,
        gender      TEXT NOT NULL DEFAULT 'Any',
        is_private  BOOLEAN NOT NULL DEFAULT false,
        is_banned   BOOLEAN NOT NULL DEFAULT false,
        created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`,

	`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_google_id ON users (google_id)`,

	`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users (username) WHERE username IS NOT NULL`,

	`CREATE INDEX IF NOT EXISTS idx_users_username_trgm ON users USING gin (username gin_trgm_ops) WHERE username IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_users_name_trgm ON users USING gin (name gin_trgm_ops)`,

	`CREATE INDEX IF NOT EXISTS idx_users_not_banned ON users (id) WHERE is_banned = false`,

	// ===================================================================
	// TABLE: device_tokens
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS device_tokens (
        id          BIGSERIAL PRIMARY KEY,
        user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        token       TEXT NOT NULL,
        device_type TEXT NOT NULL,
        updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`,

	`CREATE UNIQUE INDEX IF NOT EXISTS idx_device_tokens_token ON device_tokens (token)`,
	`CREATE INDEX IF NOT EXISTS idx_device_tokens_user_id ON device_tokens (user_id)`,

	// ===================================================================
	// TABLE: rooms
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS rooms (
        id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
        name            TEXT,
        type            TEXT NOT NULL DEFAULT 'DM',
        last_message_at TIMESTAMPTZ
    )`,

	`CREATE INDEX IF NOT EXISTS idx_rooms_last_message_at ON rooms (last_message_at DESC NULLS LAST)`,

	`CREATE INDEX IF NOT EXISTS idx_rooms_type ON rooms (type)`,

	// ===================================================================
	// TABLE: room_members (junction: users <-> rooms)
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS room_members (
        room_id      UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
        user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        last_read_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        status       TEXT NOT NULL DEFAULT 'active',
        PRIMARY KEY (room_id, user_id)
    )`,

	`CREATE INDEX IF NOT EXISTS idx_room_members_user_id ON room_members (user_id)`,

	// Covering index for the auto-subscribe query:
	// SELECT room_id FROM room_members WHERE user_id = $1 AND status = 'active'
	// This enables an index-only scan (no heap lookups) on every WS connect.
	`CREATE INDEX IF NOT EXISTS idx_room_members_user_active ON room_members (user_id) INCLUDE (room_id) WHERE status = 'active'`,

	// ===================================================================
	// TABLE: messages
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS messages (
        id         BIGSERIAL PRIMARY KEY,
        room_id    UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
        sender_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        content    TEXT NOT NULL,
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`,

	`CREATE INDEX IF NOT EXISTS idx_messages_room_id_id_desc ON messages (room_id, id DESC)`,

	`CREATE INDEX IF NOT EXISTS idx_messages_room_id_created_at_desc ON messages (room_id, created_at DESC)`,

	// ===================================================================
	// TABLE: blocked_users
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS blocked_users (
        blocker_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        blocked_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        PRIMARY KEY (blocker_id, blocked_id)
    )`,

	`CREATE INDEX IF NOT EXISTS idx_blocked_users_reverse ON blocked_users (blocked_id, blocker_id)`,

	// ===================================================================
	// TABLE: user_reports
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS user_reports (
        id          BIGSERIAL PRIMARY KEY,
        reporter_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        reported_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        reason      TEXT NOT NULL,
        created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`,

	`CREATE INDEX IF NOT EXISTS idx_user_reports_reported_id ON user_reports (reported_id)`,

	// ===================================================================
	// TABLE: friend_requests
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS friend_requests (
        id              BIGSERIAL PRIMARY KEY,
        sender_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        receiver_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        status          TEXT NOT NULL DEFAULT 'pending',
        stranger_room_id TEXT,
        created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`,

	`CREATE UNIQUE INDEX IF NOT EXISTS idx_friend_requests_pair ON friend_requests (sender_id, receiver_id)`,
	`CREATE INDEX IF NOT EXISTS idx_friend_requests_receiver ON friend_requests (receiver_id, status)`,
	`CREATE INDEX IF NOT EXISTS idx_friend_requests_sender ON friend_requests (sender_id, status)`,

	// ===================================================================
	// TABLE: friendships (sorted UUIDs prevent A↔B + B↔A duplication)
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS friendships (
        user_id_1  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        user_id_2  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        PRIMARY KEY (user_id_1, user_id_2),
        CHECK (user_id_1 < user_id_2)
    )`,

	`CREATE INDEX IF NOT EXISTS idx_friendships_reverse ON friendships (user_id_2, user_id_1)`,

	// ===================================================================
	// TRIGGER: Auto-update rooms.last_message_at on new message
	// ===================================================================
	`CREATE OR REPLACE FUNCTION update_room_last_message_at()
    RETURNS TRIGGER AS $$
    BEGIN
        UPDATE rooms SET last_message_at = NEW.created_at WHERE id = NEW.room_id;
        RETURN NEW;
    END;
    $$ LANGUAGE plpgsql`,

	`DROP TRIGGER IF EXISTS trg_update_room_last_message_at ON messages`,

	`CREATE TRIGGER trg_update_room_last_message_at
        AFTER INSERT ON messages
        FOR EACH ROW EXECUTE FUNCTION update_room_last_message_at()`,

	// ===================================================================
	// TRIGGER: Auto-update users.updated_at on any change
	// ===================================================================
	`CREATE OR REPLACE FUNCTION update_updated_at_column()
    RETURNS TRIGGER AS $$
    BEGIN
        NEW.updated_at = NOW();
        RETURN NEW;
    END;
    $$ LANGUAGE plpgsql`,

	`DROP TRIGGER IF EXISTS trg_users_updated_at ON users`,

	`CREATE TRIGGER trg_users_updated_at
        BEFORE UPDATE ON users
        FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()`,

	// ===================================================================
	// COLUMN: room_members.last_delivered_at (delivery receipts)
	// ===================================================================
	`ALTER TABLE room_members ADD COLUMN IF NOT EXISTS last_delivered_at TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01T00:00:00Z'`,

	// ===================================================================
	// COLUMN: users.last_seen_at (presence / last-seen feature)
	// ===================================================================
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`,

	// ===================================================================
	// COLUMN: users.show_last_seen (privacy toggle for online/last-seen)
	// ===================================================================
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS show_last_seen BOOLEAN NOT NULL DEFAULT true`,

	// ===================================================================
	// TABLE: call_logs (call history for rooms)
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS call_logs (
        id               BIGSERIAL PRIMARY KEY,
        call_id          TEXT NOT NULL,
        room_id          UUID REFERENCES rooms(id) ON DELETE CASCADE,
        initiated_by     UUID REFERENCES users(id) ON DELETE CASCADE,
        call_type        TEXT NOT NULL DEFAULT 'video',
        tier             TEXT NOT NULL DEFAULT 'p2p',
        max_participants INT NOT NULL DEFAULT 2,
        started_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        ended_at         TIMESTAMPTZ,
        duration_seconds INT
    )`,

	`CREATE INDEX IF NOT EXISTS idx_call_logs_room_id ON call_logs (room_id, started_at DESC)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_call_logs_call_id ON call_logs (call_id)`,

	// --- Migrations / Alterations ---
	`DO $$ BEGIN IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='call_logs' AND column_name='call_id' AND data_type='uuid') THEN ALTER TABLE call_logs ALTER COLUMN call_id TYPE TEXT; END IF; END $$`,

	// ===================================================================
	// GROUP CHAT SUPPORT — rooms augmentation
	// ===================================================================

	// Add group-specific columns to rooms
	`ALTER TABLE rooms ADD COLUMN IF NOT EXISTS avatar_url TEXT DEFAULT ''`,
	`ALTER TABLE rooms ADD COLUMN IF NOT EXISTS created_by UUID REFERENCES users(id) ON DELETE SET NULL`,
	`ALTER TABLE rooms ADD COLUMN IF NOT EXISTS max_members INT NOT NULL DEFAULT 50`,

	// Add role column to room_members (admin / member)
	`ALTER TABLE room_members ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'member'`,

	// Add join/leave timestamps for membership history
	`ALTER TABLE room_members ADD COLUMN IF NOT EXISTS joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`,
	`ALTER TABLE room_members ADD COLUMN IF NOT EXISTS left_at TIMESTAMPTZ`,

	// Partial index for fast active member listing per room
	`CREATE INDEX IF NOT EXISTS idx_room_members_active ON room_members (room_id) WHERE status = 'active'`,

	// Group visibility: public (anyone can join/discover) or private (invite only)
	`ALTER TABLE rooms ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'public'`,

	// Invite code for join-by-link (unique per room, nullable)
	`ALTER TABLE rooms ADD COLUMN IF NOT EXISTS invite_code TEXT`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_rooms_invite_code ON rooms (invite_code) WHERE invite_code IS NOT NULL`,

	// Index for listing/searching public groups
	`CREATE INDEX IF NOT EXISTS idx_rooms_visibility ON rooms (visibility) WHERE type = 'GROUP'`,

	// ===================================================================
	// P2P CALL — add peer_id for direct peer lookup in call history
	// ===================================================================
	`ALTER TABLE call_logs ADD COLUMN IF NOT EXISTS peer_id UUID REFERENCES users(id) ON DELETE SET NULL`,

	// ===================================================================
	// GROUP CALL SUPPORT — call_logs augmentation
	// ===================================================================

	// Add participants array to track who joined group calls
	`ALTER TABLE call_logs ADD COLUMN IF NOT EXISTS participants TEXT[] DEFAULT '{}'`,

	// Relax max_participants default to 50 for group calls
	`ALTER TABLE call_logs ALTER COLUMN max_participants SET DEFAULT 50`,

	// ===================================================================
	// PERFORMANCE INDEX: call_logs by initiator (for GetCallHistoryHandler)
	// The OR EXISTS query benefits from a covering index on initiated_by
	// to avoid sequential scans of the call_logs table.
	// ===================================================================
	`CREATE INDEX IF NOT EXISTS idx_call_logs_initiated_by ON call_logs (initiated_by, started_at DESC)`,

	// ===================================================================
	// TABLE: donations (tracks all user donations for badges & leaderboard)
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS donations (
		id               BIGSERIAL PRIMARY KEY,
		user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		amount           NUMERIC(10,2) NOT NULL,
		currency         VARCHAR(3) NOT NULL DEFAULT 'INR',
		payment_id       TEXT NOT NULL,
		payment_provider TEXT NOT NULL,
		created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	`CREATE INDEX IF NOT EXISTS idx_donations_user_id ON donations (user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_donations_created_at ON donations (created_at DESC)`,
	// Covering index for admin dashboard: SUM(amount) GROUP BY user_id
	`CREATE INDEX IF NOT EXISTS idx_donations_user_amount ON donations (user_id, amount)`,

	// Index for flusher unread count query: WHERE sender_id != $1
	`CREATE INDEX IF NOT EXISTS idx_messages_sender_id ON messages (sender_id)`,

	// ===================================================================
	// TABLE: badge_tiers (configurable badge tiers managed by BENKI_ADMIN)
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS badge_tiers (
		id            SERIAL PRIMARY KEY,
		name          VARCHAR(50) NOT NULL,
		min_amount    NUMERIC(10,2) NOT NULL,
		icon          TEXT NOT NULL DEFAULT '',
		display_order INT NOT NULL DEFAULT 0,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Seed default badge tiers (only if table is empty)
	`INSERT INTO badge_tiers (name, min_amount, icon, display_order)
	 SELECT * FROM (VALUES
		('Bronze Supporter',  50.00,   'bronze_icon',  1),
		('Silver Supporter',  200.00,  'silver_icon',  2),
		('Gold Supporter',    500.00,  'gold_icon',    3),
		('Founding Member',   1000.00, 'crown_icon',   4)
	 ) AS v(name, min_amount, icon, display_order)
	 WHERE NOT EXISTS (SELECT 1 FROM badge_tiers)`,

	// ===================================================================
	// TABLE: app_settings (key-value config managed by BENKI_ADMIN)
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS app_settings (
		key        TEXT PRIMARY KEY,
		value      JSONB NOT NULL DEFAULT '{}',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Seed default settings
	`INSERT INTO app_settings (key, value) VALUES
		('leaderboard_config', '{"monthly_reset_day": 1, "max_entries": 50, "enabled": true}'),
		('group_capacity', '{"free_max": 50, "vip_max": 500}'),
		('premium_connect', '{"min_donation": 50}')
	 ON CONFLICT (key) DO NOTHING`,

	// ===================================================================
	// COLUMN: rooms.vanity_slug (vanity URLs for groups)
	// ===================================================================
	`ALTER TABLE rooms ADD COLUMN IF NOT EXISTS vanity_slug VARCHAR(50)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_rooms_vanity_slug ON rooms (vanity_slug) WHERE vanity_slug IS NOT NULL`,

	// ===================================================================
	// COLUMN: friend_requests.is_premium (premium connect feature)
	// ===================================================================
	`ALTER TABLE friend_requests ADD COLUMN IF NOT EXISTS is_premium BOOLEAN NOT NULL DEFAULT false`,

	// ===================================================================
	// TABLE: pending_orders (maps Razorpay orders to users for webhook)
	// ===================================================================
	`CREATE TABLE IF NOT EXISTS pending_orders (
		id                 BIGSERIAL PRIMARY KEY,
		order_id           TEXT NOT NULL,
		razorpay_order_id  TEXT,
		user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		amount             BIGINT NOT NULL,
		currency           VARCHAR(3) NOT NULL DEFAULT 'INR',
		status             TEXT NOT NULL DEFAULT 'pending',
		created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	`CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_orders_order_id ON pending_orders (order_id)`,
	`CREATE INDEX IF NOT EXISTS idx_pending_orders_user_id ON pending_orders (user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_pending_orders_razorpay_order_id ON pending_orders (razorpay_order_id) WHERE razorpay_order_id IS NOT NULL`,

	// ===================================================================
	// COLUMN: donations.razorpay_order_id (link donation to Razorpay order)
	// ===================================================================
	`ALTER TABLE donations ADD COLUMN IF NOT EXISTS razorpay_order_id TEXT`,

	// ===================================================================
	// THE ARENA: Extensions + Tables
	// ===================================================================

	// ltree extension for nested comment trees
	`CREATE EXTENSION IF NOT EXISTS "ltree"`,

	// TABLE: posts
	`CREATE TABLE IF NOT EXISTS posts (
		id               BIGSERIAL PRIMARY KEY,
		user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		caption          TEXT NOT NULL DEFAULT '',
		post_type        TEXT NOT NULL DEFAULT 'original',
		original_post_id BIGINT REFERENCES posts(id) ON DELETE SET NULL,
		visibility       TEXT NOT NULL DEFAULT 'public',
		is_pinned        BOOLEAN NOT NULL DEFAULT false,
		like_count       INT NOT NULL DEFAULT 0,
		comment_count    INT NOT NULL DEFAULT 0,
		repost_count     INT NOT NULL DEFAULT 0,
		created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_posts_user_id ON posts (user_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_posts_created_at ON posts (created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_posts_visibility ON posts (visibility) WHERE visibility = 'public'`,

	// TABLE: post_media (carousel items)
	`CREATE TABLE IF NOT EXISTS post_media (
		id           BIGSERIAL PRIMARY KEY,
		post_id      BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		media_type   TEXT NOT NULL,
		object_key   TEXT NOT NULL,
		width        SMALLINT NOT NULL DEFAULT 0,
		height       SMALLINT NOT NULL DEFAULT 0,
		duration_ms  INT,
		preview_hash TEXT,
		sort_order   SMALLINT NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_post_media_post_id ON post_media (post_id, sort_order)`,

	// TABLE: post_likes
	`CREATE TABLE IF NOT EXISTS post_likes (
		user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		post_id    BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (user_id, post_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_post_likes_post_id ON post_likes (post_id, created_at DESC)`,

	// Trigger: increment/decrement posts.like_count on post_likes insert/delete
	`CREATE OR REPLACE FUNCTION update_post_like_count() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE posts SET like_count = like_count + 1 WHERE id = NEW.post_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE posts SET like_count = like_count - 1 WHERE id = OLD.post_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql`,
	`DROP TRIGGER IF EXISTS trg_post_like_count ON post_likes`,
	`CREATE TRIGGER trg_post_like_count AFTER INSERT OR DELETE ON post_likes
	 FOR EACH ROW EXECUTE FUNCTION update_post_like_count()`,

	// TABLE: post_comments (nested via ltree)
	`CREATE TABLE IF NOT EXISTS post_comments (
		id           BIGSERIAL PRIMARY KEY,
		post_id      BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		body         TEXT NOT NULL DEFAULT '',
		path         ltree NOT NULL,
		depth        SMALLINT NOT NULL DEFAULT 0,
		like_count   INT NOT NULL DEFAULT 0,
		gif_url      TEXT,
		gif_width    SMALLINT,
		gif_height   SMALLINT,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_post_comments_post_id ON post_comments (post_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_post_comments_path ON post_comments USING GIST (path)`,
	`CREATE INDEX IF NOT EXISTS idx_post_comments_depth ON post_comments (post_id, depth, like_count DESC, created_at)`,

	// reply_count: materialized count of direct replies (maintained by trigger)
	`ALTER TABLE post_comments ADD COLUMN IF NOT EXISTS reply_count INT NOT NULL DEFAULT 0`,

	// Trigger: increment/decrement parent's reply_count when a reply is inserted/deleted/path updated
	`CREATE OR REPLACE FUNCTION update_comment_reply_count() RETURNS TRIGGER AS $$
	DECLARE parent_id BIGINT;
	BEGIN
		IF TG_OP = 'UPDATE' AND NEW.depth > 0 AND nlevel(OLD.path) = 0 AND nlevel(NEW.path) > 0 THEN
			-- Path was just assigned (INSERT sets path='', then UPDATE sets real path)
			SELECT id INTO parent_id FROM post_comments
			  WHERE post_id = NEW.post_id AND path = subpath(NEW.path, 0, nlevel(NEW.path)-1);
			IF parent_id IS NOT NULL THEN
				UPDATE post_comments SET reply_count = reply_count + 1 WHERE id = parent_id;
			END IF;
		ELSIF TG_OP = 'DELETE' AND OLD.depth > 0 AND nlevel(OLD.path) > 0 THEN
			SELECT id INTO parent_id FROM post_comments
			  WHERE post_id = OLD.post_id AND path = subpath(OLD.path, 0, nlevel(OLD.path)-1);
			IF parent_id IS NOT NULL THEN
				UPDATE post_comments SET reply_count = GREATEST(reply_count - 1, 0) WHERE id = parent_id;
			END IF;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql`,
	`DROP TRIGGER IF EXISTS trg_comment_reply_count ON post_comments`,
	`CREATE TRIGGER trg_comment_reply_count AFTER DELETE OR UPDATE OF path ON post_comments
	 FOR EACH ROW EXECUTE FUNCTION update_comment_reply_count()`,

	// Backfill reply_count for any existing comments
	`UPDATE post_comments SET reply_count = COALESCE(sub.cnt, 0)
	 FROM (
	   SELECT parent.id, COUNT(child.id) AS cnt
	   FROM post_comments parent
	   JOIN post_comments child ON child.post_id = parent.post_id
	     AND child.path <@ parent.path AND child.path != parent.path
	     AND child.depth = parent.depth + 1
	   GROUP BY parent.id
	 ) sub
	 WHERE post_comments.id = sub.id AND post_comments.reply_count != sub.cnt`,

	// Trigger: increment/decrement posts.comment_count on post_comments insert/delete
	`CREATE OR REPLACE FUNCTION update_post_comment_count() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE posts SET comment_count = comment_count + 1 WHERE id = NEW.post_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE posts SET comment_count = comment_count - 1 WHERE id = OLD.post_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql`,
	`DROP TRIGGER IF EXISTS trg_post_comment_count ON post_comments`,
	`CREATE TRIGGER trg_post_comment_count AFTER INSERT OR DELETE ON post_comments
	 FOR EACH ROW EXECUTE FUNCTION update_post_comment_count()`,

	// TABLE: comment_likes
	`CREATE TABLE IF NOT EXISTS comment_likes (
		user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		comment_id BIGINT NOT NULL REFERENCES post_comments(id) ON DELETE CASCADE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (user_id, comment_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_comment_likes_comment_id ON comment_likes (comment_id)`,

	// Trigger: increment/decrement post_comments.like_count on comment_likes insert/delete
	`CREATE OR REPLACE FUNCTION update_comment_like_count() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE post_comments SET like_count = like_count + 1 WHERE id = NEW.comment_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE post_comments SET like_count = like_count - 1 WHERE id = OLD.comment_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql`,
	`DROP TRIGGER IF EXISTS trg_comment_like_count ON comment_likes`,
	`CREATE TRIGGER trg_comment_like_count AFTER INSERT OR DELETE ON comment_likes
	 FOR EACH ROW EXECUTE FUNCTION update_comment_like_count()`,

	// TABLE: post_reports
	`CREATE TABLE IF NOT EXISTS post_reports (
		id          BIGSERIAL PRIMARY KEY,
		reporter_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		post_id     BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		reason      TEXT NOT NULL,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_post_reports_post ON post_reports (post_id)`,

	// TABLE: comment_reports
	`CREATE TABLE IF NOT EXISTS comment_reports (
		id          BIGSERIAL PRIMARY KEY,
		reporter_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		comment_id  BIGINT NOT NULL REFERENCES post_comments(id) ON DELETE CASCADE,
		reason      TEXT NOT NULL,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_comment_reports_comment ON comment_reports (comment_id)`,

	// Repost dedup index (covers WHERE user_id = $1 AND original_post_id = $2 AND post_type = 'repost')
	`CREATE INDEX IF NOT EXISTS idx_posts_repost_dedup ON posts (user_id, original_post_id) WHERE post_type = 'repost'`,

	// Missing indexes for scale
	`CREATE INDEX IF NOT EXISTS idx_call_logs_peer_id ON call_logs (peer_id, started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_posts_user_pinned ON posts (user_id, is_pinned DESC, created_at DESC)`,

	// ---------------------------------------------------------------------------
	// Materialized counters & cached columns for 10 lakh+ scale
	// ---------------------------------------------------------------------------

	// member_count on rooms: avoids COUNT(*) FROM room_members per row
	`ALTER TABLE rooms ADD COLUMN IF NOT EXISTS member_count INT NOT NULL DEFAULT 0`,
	// Backfill existing rooms
	`UPDATE rooms SET member_count = (SELECT COUNT(*) FROM room_members rm WHERE rm.room_id = rooms.id AND rm.status = 'active') WHERE member_count = 0 AND type = 'GROUP'`,
	// Trigger: keep member_count in sync on room_members changes
	`CREATE OR REPLACE FUNCTION update_room_member_count() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' AND NEW.status = 'active' THEN
			UPDATE rooms SET member_count = member_count + 1 WHERE id = NEW.room_id;
		ELSIF TG_OP = 'DELETE' AND OLD.status = 'active' THEN
			UPDATE rooms SET member_count = GREATEST(member_count - 1, 0) WHERE id = OLD.room_id;
		ELSIF TG_OP = 'UPDATE' THEN
			IF OLD.status != 'active' AND NEW.status = 'active' THEN
				UPDATE rooms SET member_count = member_count + 1 WHERE id = NEW.room_id;
			ELSIF OLD.status = 'active' AND NEW.status != 'active' THEN
				UPDATE rooms SET member_count = GREATEST(member_count - 1, 0) WHERE id = OLD.room_id;
			END IF;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql`,
	`DROP TRIGGER IF EXISTS trg_room_member_count ON room_members`,
	`CREATE TRIGGER trg_room_member_count AFTER INSERT OR DELETE OR UPDATE OF status ON room_members
	 FOR EACH ROW EXECUTE FUNCTION update_room_member_count()`,

	// total_donated on users: avoids SUM(amount) FROM donations per request
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS total_donated NUMERIC(12,2) NOT NULL DEFAULT 0`,
	// Backfill existing users
	`UPDATE users SET total_donated = COALESCE((SELECT SUM(amount) FROM donations d WHERE d.user_id = users.id), 0) WHERE total_donated = 0`,
	// Trigger: keep total_donated in sync on donations insert/delete
	`CREATE OR REPLACE FUNCTION update_user_total_donated() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE users SET total_donated = total_donated + NEW.amount WHERE id = NEW.user_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE users SET total_donated = GREATEST(total_donated - OLD.amount, 0) WHERE id = OLD.user_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql`,
	`DROP TRIGGER IF EXISTS trg_user_total_donated ON donations`,
	`CREATE TRIGGER trg_user_total_donated AFTER INSERT OR DELETE ON donations
	 FOR EACH ROW EXECUTE FUNCTION update_user_total_donated()`,

	// Expression index for trending feed sort (avoids seq scan + sort)
	`CREATE INDEX IF NOT EXISTS idx_posts_trending ON posts ((like_count + comment_count * 2 + repost_count * 3), created_at DESC) WHERE visibility = 'public'`,

	// report_count on users: avoids COUNT(*) FROM user_reports per row in admin panel
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS report_count INT NOT NULL DEFAULT 0`,
	`UPDATE users SET report_count = (SELECT COUNT(*) FROM user_reports ur WHERE ur.reported_id = users.id) WHERE report_count = 0`,
	`CREATE OR REPLACE FUNCTION update_user_report_count() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE users SET report_count = report_count + 1 WHERE id = NEW.reported_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE users SET report_count = GREATEST(report_count - 1, 0) WHERE id = OLD.reported_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql`,
	`DROP TRIGGER IF EXISTS trg_user_report_count ON user_reports`,
	`CREATE TRIGGER trg_user_report_count AFTER INSERT OR DELETE ON user_reports
	 FOR EACH ROW EXECUTE FUNCTION update_user_report_count()`,

	// last_message_preview on rooms: avoids LATERAL subquery scanning messages per room
	`ALTER TABLE rooms ADD COLUMN IF NOT EXISTS last_message_preview TEXT DEFAULT ''`,

	// Trigger: update last_message_preview on new message insert (already has last_message_at trigger)
	`CREATE OR REPLACE FUNCTION update_room_last_message_preview() RETURNS TRIGGER AS $$
	BEGIN
		UPDATE rooms SET last_message_preview = LEFT(NEW.content, 100) WHERE id = NEW.room_id;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql`,
	`DROP TRIGGER IF EXISTS trg_room_last_message_preview ON messages`,
	`CREATE TRIGGER trg_room_last_message_preview AFTER INSERT ON messages
	 FOR EACH ROW EXECUTE FUNCTION update_room_last_message_preview()`,

	// GIN trigram indexes for ILIKE/LIKE searches at scale
	`CREATE INDEX IF NOT EXISTS idx_users_name_trgm ON users USING gin (name gin_trgm_ops)`,
	`CREATE INDEX IF NOT EXISTS idx_users_username_trgm ON users USING gin (username gin_trgm_ops)`,
	`CREATE INDEX IF NOT EXISTS idx_rooms_name_trgm ON rooms USING gin (name gin_trgm_ops)`,

	// Seed: arena_limits (admin-configurable post limits + upload sizes)
	`INSERT INTO app_settings (key, value) VALUES
		('arena_limits', '{"max_posts_per_user": 50, "max_media_per_post": 10, "max_image_size_kb": 100, "max_video_size_kb": 500, "max_caption_length": 2200, "max_comment_length": 1000, "free_caption_length": 300, "free_comment_length": 200, "trending_threshold": 50, "presign_put_mins": 5, "presign_get_mins": 30}')
	 ON CONFLICT (key) DO NOTHING`,
}

// RunMigrations executes all DDL statements sequentially.
// Panics on failure because the app cannot function without the schema.
func RunMigrations() {
	db := postgress.GetRawDB()

	for i, stmt := range migrationStatements {
		if _, err := db.Exec(stmt); err != nil {
			log.Fatalf("[migration] Statement %d failed: %v\nSQL: %s", i+1, err, stmt)
		}
	}

	log.Printf("[migration] All %d statements executed successfully", len(migrationStatements))
}
