CREATE SCHEMA IF NOT EXISTS zquab;
SET search_path TO zquab, public;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE EXTENSION IF NOT EXISTS "pg_trgm";

CREATE TABLE IF NOT EXISTS users (
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
    );

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_google_id ON users (google_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users (username) WHERE username IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_users_username_trgm ON users USING gin (username gin_trgm_ops) WHERE username IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_users_name_trgm ON users USING gin (name gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_users_not_banned ON users (id) WHERE is_banned = false;

CREATE TABLE IF NOT EXISTS device_tokens (
        id          BIGSERIAL PRIMARY KEY,
        user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        token       TEXT NOT NULL,
        device_type TEXT NOT NULL,
        updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );

CREATE UNIQUE INDEX IF NOT EXISTS idx_device_tokens_token ON device_tokens (token);

CREATE INDEX IF NOT EXISTS idx_device_tokens_user_id ON device_tokens (user_id);

CREATE TABLE IF NOT EXISTS rooms (
        id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
        name            TEXT,
        type            TEXT NOT NULL DEFAULT 'DM',
        last_message_at TIMESTAMPTZ
    );

CREATE INDEX IF NOT EXISTS idx_rooms_last_message_at ON rooms (last_message_at DESC NULLS LAST);

CREATE INDEX IF NOT EXISTS idx_rooms_type ON rooms (type);

CREATE TABLE IF NOT EXISTS room_members (
        room_id      UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
        user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        last_read_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        status       TEXT NOT NULL DEFAULT 'active',
        PRIMARY KEY (room_id, user_id)
    );

CREATE INDEX IF NOT EXISTS idx_room_members_user_id ON room_members (user_id);

CREATE INDEX IF NOT EXISTS idx_room_members_user_active ON room_members (user_id) INCLUDE (room_id) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS messages (
        id         BIGSERIAL PRIMARY KEY,
        room_id    UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
        sender_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        content    TEXT NOT NULL,
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );

CREATE INDEX IF NOT EXISTS idx_messages_room_id_id_desc ON messages (room_id, id DESC);

CREATE INDEX IF NOT EXISTS idx_messages_room_id_created_at_desc ON messages (room_id, created_at DESC);

CREATE TABLE IF NOT EXISTS blocked_users (
        blocker_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        blocked_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        PRIMARY KEY (blocker_id, blocked_id)
    );

CREATE INDEX IF NOT EXISTS idx_blocked_users_reverse ON blocked_users (blocked_id, blocker_id);

CREATE TABLE IF NOT EXISTS user_reports (
        id          BIGSERIAL PRIMARY KEY,
        reporter_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        reported_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        reason      TEXT NOT NULL,
        created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );

CREATE INDEX IF NOT EXISTS idx_user_reports_reported_id ON user_reports (reported_id);

CREATE TABLE IF NOT EXISTS friend_requests (
        id              BIGSERIAL PRIMARY KEY,
        sender_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        receiver_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        status          TEXT NOT NULL DEFAULT 'pending',
        stranger_room_id TEXT,
        created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );

CREATE UNIQUE INDEX IF NOT EXISTS idx_friend_requests_pair ON friend_requests (sender_id, receiver_id);

CREATE INDEX IF NOT EXISTS idx_friend_requests_receiver ON friend_requests (receiver_id, status);

CREATE INDEX IF NOT EXISTS idx_friend_requests_sender ON friend_requests (sender_id, status);

CREATE TABLE IF NOT EXISTS friendships (
        user_id_1  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        user_id_2  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        PRIMARY KEY (user_id_1, user_id_2),
        CHECK (user_id_1 < user_id_2)
    );

CREATE INDEX IF NOT EXISTS idx_friendships_reverse ON friendships (user_id_2, user_id_1);

CREATE OR REPLACE FUNCTION update_room_last_message_at()
    RETURNS TRIGGER AS $$
    BEGIN
        UPDATE rooms SET last_message_at = NEW.created_at WHERE id = NEW.room_id;
        RETURN NEW;
    END;
    $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_update_room_last_message_at ON messages;

CREATE TRIGGER trg_update_room_last_message_at
        AFTER INSERT ON messages
        FOR EACH ROW EXECUTE FUNCTION update_room_last_message_at();

CREATE OR REPLACE FUNCTION update_updated_at_column()
    RETURNS TRIGGER AS $$
    BEGIN
        NEW.updated_at = NOW();
        RETURN NEW;
    END;
    $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_updated_at ON users;

CREATE TRIGGER trg_users_updated_at
        BEFORE UPDATE ON users
        FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE room_members ADD COLUMN IF NOT EXISTS last_delivered_at TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01T00:00:00Z';

ALTER TABLE users ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE users ADD COLUMN IF NOT EXISTS show_last_seen BOOLEAN NOT NULL DEFAULT true;

CREATE TABLE IF NOT EXISTS call_logs (
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
    );

CREATE INDEX IF NOT EXISTS idx_call_logs_room_id ON call_logs (room_id, started_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_call_logs_call_id ON call_logs (call_id);

DO $$ BEGIN IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='call_logs' AND column_name='call_id' AND data_type='uuid') THEN ALTER TABLE call_logs ALTER COLUMN call_id TYPE TEXT; END IF; END $$;

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS avatar_url TEXT DEFAULT '';

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS created_by UUID REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS max_members INT NOT NULL DEFAULT 50;

ALTER TABLE room_members ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'member';

ALTER TABLE room_members ADD COLUMN IF NOT EXISTS joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE room_members ADD COLUMN IF NOT EXISTS left_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_room_members_active ON room_members (room_id) WHERE status = 'active';

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'public';

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS invite_code TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_rooms_invite_code ON rooms (invite_code) WHERE invite_code IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_rooms_visibility ON rooms (visibility) WHERE type = 'GROUP';

ALTER TABLE call_logs ADD COLUMN IF NOT EXISTS peer_id UUID REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE call_logs ADD COLUMN IF NOT EXISTS participants TEXT[] DEFAULT '{}';

ALTER TABLE call_logs ALTER COLUMN max_participants SET DEFAULT 50;

CREATE INDEX IF NOT EXISTS idx_call_logs_initiated_by ON call_logs (initiated_by, started_at DESC);

CREATE TABLE IF NOT EXISTS donations (
		id               BIGSERIAL PRIMARY KEY,
		user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		amount           NUMERIC(10,2) NOT NULL,
		currency         VARCHAR(3) NOT NULL DEFAULT 'INR',
		payment_id       TEXT NOT NULL,
		payment_provider TEXT NOT NULL,
		created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

CREATE INDEX IF NOT EXISTS idx_donations_user_id ON donations (user_id);

CREATE INDEX IF NOT EXISTS idx_donations_created_at ON donations (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_donations_user_amount ON donations (user_id, amount);

CREATE INDEX IF NOT EXISTS idx_messages_sender_id ON messages (sender_id);

CREATE TABLE IF NOT EXISTS badge_tiers (
		id            SERIAL PRIMARY KEY,
		name          VARCHAR(50) NOT NULL,
		min_amount    NUMERIC(10,2) NOT NULL,
		icon          TEXT NOT NULL DEFAULT '',
		display_order INT NOT NULL DEFAULT 0,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

INSERT INTO badge_tiers (name, min_amount, icon, display_order)
	 SELECT * FROM (VALUES
		('Bronze Supporter',  50.00,   'bronze_icon',  1),
		('Silver Supporter',  200.00,  'silver_icon',  2),
		('Gold Supporter',    500.00,  'gold_icon',    3),
		('Founding Member',   1000.00, 'crown_icon',   4)
	 ) AS v(name, min_amount, icon, display_order)
	 WHERE NOT EXISTS (SELECT 1 FROM badge_tiers);

CREATE TABLE IF NOT EXISTS app_settings (
		key        TEXT PRIMARY KEY,
		value      JSONB NOT NULL DEFAULT '{}',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

INSERT INTO app_settings (key, value) VALUES
		('leaderboard_config', '{"monthly_reset_day": 1, "max_entries": 50, "enabled": true}'),
		('group_capacity', '{"free_max": 50, "vip_max": 500}'),
		('premium_connect', '{"min_donation": 50}')
	 ON CONFLICT (key) DO NOTHING;

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS vanity_slug VARCHAR(50);

CREATE UNIQUE INDEX IF NOT EXISTS idx_rooms_vanity_slug ON rooms (vanity_slug) WHERE vanity_slug IS NOT NULL;

ALTER TABLE friend_requests ADD COLUMN IF NOT EXISTS is_premium BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS pending_orders (
		id                 BIGSERIAL PRIMARY KEY,
		order_id           TEXT NOT NULL,
		razorpay_order_id  TEXT,
		user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		amount             BIGINT NOT NULL,
		currency           VARCHAR(3) NOT NULL DEFAULT 'INR',
		status             TEXT NOT NULL DEFAULT 'pending',
		created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_orders_order_id ON pending_orders (order_id);

CREATE INDEX IF NOT EXISTS idx_pending_orders_user_id ON pending_orders (user_id);

CREATE INDEX IF NOT EXISTS idx_pending_orders_razorpay_order_id ON pending_orders (razorpay_order_id) WHERE razorpay_order_id IS NOT NULL;

ALTER TABLE donations ADD COLUMN IF NOT EXISTS razorpay_order_id TEXT;

CREATE EXTENSION IF NOT EXISTS "ltree";

CREATE TABLE IF NOT EXISTS posts (
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
	);

CREATE INDEX IF NOT EXISTS idx_posts_user_id ON posts (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_posts_created_at ON posts (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_posts_visibility ON posts (visibility) WHERE visibility = 'public';

CREATE TABLE IF NOT EXISTS post_media (
		id           BIGSERIAL PRIMARY KEY,
		post_id      BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		media_type   TEXT NOT NULL,
		object_key   TEXT NOT NULL,
		width        SMALLINT NOT NULL DEFAULT 0,
		height       SMALLINT NOT NULL DEFAULT 0,
		duration_ms  INT,
		preview_hash TEXT,
		sort_order   SMALLINT NOT NULL DEFAULT 0
	);

CREATE INDEX IF NOT EXISTS idx_post_media_post_id ON post_media (post_id, sort_order);

CREATE TABLE IF NOT EXISTS post_likes (
		user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		post_id    BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (user_id, post_id)
	);

CREATE INDEX IF NOT EXISTS idx_post_likes_post_id ON post_likes (post_id, created_at DESC);

DROP TRIGGER IF EXISTS trg_post_like_count ON post_likes;

DROP FUNCTION IF EXISTS update_post_like_count();

CREATE TABLE IF NOT EXISTS post_comments (
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
	);

CREATE INDEX IF NOT EXISTS idx_post_comments_post_id ON post_comments (post_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_post_comments_path ON post_comments USING GIST (path);

CREATE INDEX IF NOT EXISTS idx_post_comments_depth ON post_comments (post_id, depth, like_count DESC, created_at);

ALTER TABLE post_comments ADD COLUMN IF NOT EXISTS reply_count INT NOT NULL DEFAULT 0;

CREATE OR REPLACE FUNCTION update_comment_reply_count() RETURNS TRIGGER AS $$
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
	$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_comment_reply_count ON post_comments;

CREATE TRIGGER trg_comment_reply_count AFTER DELETE OR UPDATE OF path ON post_comments
	 FOR EACH ROW EXECUTE FUNCTION update_comment_reply_count();

UPDATE post_comments SET reply_count = COALESCE(sub.cnt, 0)
	 FROM (
	   SELECT parent.id, COUNT(child.id) AS cnt
	   FROM post_comments parent
	   JOIN post_comments child ON child.post_id = parent.post_id
	     AND child.path <@ parent.path AND child.path != parent.path
	     AND child.depth = parent.depth + 1
	   GROUP BY parent.id
	 ) sub
	 WHERE post_comments.id = sub.id AND post_comments.reply_count != sub.cnt;

CREATE OR REPLACE FUNCTION update_post_comment_count() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE posts SET comment_count = comment_count + 1 WHERE id = NEW.post_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE posts SET comment_count = comment_count - 1 WHERE id = OLD.post_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_post_comment_count ON post_comments;

CREATE TRIGGER trg_post_comment_count AFTER INSERT OR DELETE ON post_comments
	 FOR EACH ROW EXECUTE FUNCTION update_post_comment_count();

CREATE TABLE IF NOT EXISTS comment_likes (
		user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		comment_id BIGINT NOT NULL REFERENCES post_comments(id) ON DELETE CASCADE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (user_id, comment_id)
	);

CREATE INDEX IF NOT EXISTS idx_comment_likes_comment_id ON comment_likes (comment_id);

CREATE OR REPLACE FUNCTION update_comment_like_count() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE post_comments SET like_count = like_count + 1 WHERE id = NEW.comment_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE post_comments SET like_count = like_count - 1 WHERE id = OLD.comment_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_comment_like_count ON comment_likes;

CREATE TRIGGER trg_comment_like_count AFTER INSERT OR DELETE ON comment_likes
	 FOR EACH ROW EXECUTE FUNCTION update_comment_like_count();

CREATE TABLE IF NOT EXISTS post_reports (
		id          BIGSERIAL PRIMARY KEY,
		reporter_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		post_id     BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		reason      TEXT NOT NULL,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

CREATE INDEX IF NOT EXISTS idx_post_reports_post ON post_reports (post_id);

CREATE TABLE IF NOT EXISTS comment_reports (
		id          BIGSERIAL PRIMARY KEY,
		reporter_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		comment_id  BIGINT NOT NULL REFERENCES post_comments(id) ON DELETE CASCADE,
		reason      TEXT NOT NULL,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

CREATE INDEX IF NOT EXISTS idx_comment_reports_comment ON comment_reports (comment_id);

DROP INDEX IF EXISTS idx_posts_repost_dedup;

CREATE UNIQUE INDEX IF NOT EXISTS idx_posts_repost_plain_dedup ON posts (user_id, original_post_id) WHERE post_type = 'repost' AND caption = '';

CREATE INDEX IF NOT EXISTS idx_call_logs_peer_id ON call_logs (peer_id, started_at DESC);

CREATE INDEX IF NOT EXISTS idx_posts_user_pinned ON posts (user_id, is_pinned DESC, created_at DESC);

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS member_count INT NOT NULL DEFAULT 0;

UPDATE rooms SET member_count = (SELECT COUNT(*) FROM room_members rm WHERE rm.room_id = rooms.id AND rm.status = 'active') WHERE member_count = 0 AND type = 'GROUP';

CREATE OR REPLACE FUNCTION update_room_member_count() RETURNS TRIGGER AS $$
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
	$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_room_member_count ON room_members;

CREATE TRIGGER trg_room_member_count AFTER INSERT OR DELETE OR UPDATE OF status ON room_members
	 FOR EACH ROW EXECUTE FUNCTION update_room_member_count();

ALTER TABLE users ADD COLUMN IF NOT EXISTS total_donated NUMERIC(12,2) NOT NULL DEFAULT 0;

UPDATE users SET total_donated = COALESCE((SELECT SUM(amount) FROM donations d WHERE d.user_id = users.id), 0) WHERE total_donated = 0;

CREATE OR REPLACE FUNCTION update_user_total_donated() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE users SET total_donated = total_donated + NEW.amount WHERE id = NEW.user_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE users SET total_donated = GREATEST(total_donated - OLD.amount, 0) WHERE id = OLD.user_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_user_total_donated ON donations;

CREATE TRIGGER trg_user_total_donated AFTER INSERT OR DELETE ON donations
	 FOR EACH ROW EXECUTE FUNCTION update_user_total_donated();

ALTER TABLE posts ADD COLUMN IF NOT EXISTS view_count INT NOT NULL DEFAULT 0;

ALTER TABLE posts ADD COLUMN IF NOT EXISTS bookmark_count INT NOT NULL DEFAULT 0;

ALTER TABLE posts ADD COLUMN IF NOT EXISTS quote_count INT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS post_views (
		user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		PRIMARY KEY (user_id, post_id)
	);

CREATE INDEX IF NOT EXISTS idx_post_views_post_id ON post_views (post_id);

DROP TRIGGER IF EXISTS trg_post_view_count ON post_views;

DROP FUNCTION IF EXISTS update_post_view_count();

CREATE TABLE IF NOT EXISTS post_bookmarks (
		user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		post_id    BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (user_id, post_id)
	);

CREATE INDEX IF NOT EXISTS idx_post_bookmarks_user ON post_bookmarks (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_post_bookmarks_post ON post_bookmarks (post_id);

CREATE OR REPLACE FUNCTION update_post_bookmark_count() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE posts SET bookmark_count = bookmark_count + 1 WHERE id = NEW.post_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE posts SET bookmark_count = bookmark_count - 1 WHERE id = OLD.post_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_post_bookmark_count ON post_bookmarks;

CREATE TRIGGER trg_post_bookmark_count AFTER INSERT OR DELETE ON post_bookmarks
	 FOR EACH ROW EXECUTE FUNCTION update_post_bookmark_count();

DROP INDEX IF EXISTS idx_posts_trending;

CREATE INDEX IF NOT EXISTS idx_posts_trending ON posts ((like_count + comment_count * 2 + repost_count * 3 + view_count / 10), created_at DESC) WHERE visibility = 'public';

ALTER TABLE posts ADD COLUMN IF NOT EXISTS detail_expand_count INT NOT NULL DEFAULT 0;

ALTER TABLE posts ADD COLUMN IF NOT EXISTS profile_click_count INT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS post_detail_expands (
		user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		PRIMARY KEY (user_id, post_id)
	);

CREATE INDEX IF NOT EXISTS idx_post_detail_expands_post ON post_detail_expands (post_id);

DROP TRIGGER IF EXISTS trg_post_detail_expand_count ON post_detail_expands;

DROP FUNCTION IF EXISTS update_post_detail_expand_count();

CREATE TABLE IF NOT EXISTS post_profile_clicks (
		user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		PRIMARY KEY (user_id, post_id)
	);

CREATE INDEX IF NOT EXISTS idx_post_profile_clicks_post ON post_profile_clicks (post_id);

DROP TRIGGER IF EXISTS trg_post_profile_click_count ON post_profile_clicks;

DROP FUNCTION IF EXISTS update_post_profile_click_count();

CREATE INDEX IF NOT EXISTS idx_posts_original_reposts ON posts (original_post_id, created_at DESC) WHERE post_type = 'repost' AND caption != '';

ALTER TABLE users ADD COLUMN IF NOT EXISTS report_count INT NOT NULL DEFAULT 0;

UPDATE users SET report_count = (SELECT COUNT(*) FROM user_reports ur WHERE ur.reported_id = users.id) WHERE report_count = 0;

CREATE OR REPLACE FUNCTION update_user_report_count() RETURNS TRIGGER AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			UPDATE users SET report_count = report_count + 1 WHERE id = NEW.reported_id;
		ELSIF TG_OP = 'DELETE' THEN
			UPDATE users SET report_count = GREATEST(report_count - 1, 0) WHERE id = OLD.reported_id;
		END IF;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_user_report_count ON user_reports;

CREATE TRIGGER trg_user_report_count AFTER INSERT OR DELETE ON user_reports
	 FOR EACH ROW EXECUTE FUNCTION update_user_report_count();

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS last_message_preview TEXT DEFAULT '';

CREATE OR REPLACE FUNCTION update_room_last_message_preview() RETURNS TRIGGER AS $$
	BEGIN
		UPDATE rooms SET last_message_preview = LEFT(NEW.content, 100) WHERE id = NEW.room_id;
		RETURN NULL;
	END;
	$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_room_last_message_preview ON messages;

CREATE TRIGGER trg_room_last_message_preview AFTER INSERT ON messages
	 FOR EACH ROW EXECUTE FUNCTION update_room_last_message_preview();

CREATE INDEX IF NOT EXISTS idx_users_name_trgm ON users USING gin (name gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_users_username_trgm ON users USING gin (username gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_rooms_name_trgm ON rooms USING gin (name gin_trgm_ops);

INSERT INTO app_settings (key, value) VALUES
		('arena_limits', '{"max_posts_per_user": 50, "max_media_per_post": 10, "max_image_size_kb": 100, "max_video_size_kb": 500, "max_caption_length": 2200, "max_comment_length": 1000, "free_caption_length": 300, "free_comment_length": 200, "trending_threshold": 50, "presign_put_mins": 5, "presign_get_mins": 30}')
	 ON CONFLICT (key) DO NOTHING;

ALTER TABLE users ADD COLUMN IF NOT EXISTS bio TEXT NOT NULL DEFAULT '';

ALTER TABLE users ADD COLUMN IF NOT EXISTS notification_prefs JSONB NOT NULL DEFAULT '{"likes": true, "comments": true, "friend_requests": true, "reposts": true, "dm_requests": true, "group_invites": true, "match_activity": true, "mentions": true}';

ALTER TABLE posts ADD COLUMN IF NOT EXISTS last_engaged_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_posts_last_engaged ON posts (last_engaged_at DESC) WHERE visibility = 'public';
