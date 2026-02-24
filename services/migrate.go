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
