-- Phoenix initial schema. Loaded automatically by the postgres container on
-- first boot (docker-entrypoint-initdb.d).

-- Users: identity. Guests get a row too (is_guest = true).
CREATE TABLE IF NOT EXISTS users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email       TEXT UNIQUE,
    is_guest    BOOLEAN NOT NULL DEFAULT false,
    display_name TEXT NOT NULL,
    banned      BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Refresh tokens: rotated on use; revocation = delete row.
CREATE TABLE IF NOT EXISTS refresh_tokens (
    token_hash  TEXT PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Rooms: durable room metadata. Live/ephemeral state lives in Redis.
CREATE TABLE IF NOT EXISTS rooms (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    UUID REFERENCES users(id),
    game_type   TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open',  -- open | playing | closed
    max_players INT  NOT NULL DEFAULT 8,
    is_private  BOOLEAN NOT NULL DEFAULT false,
    invite_code TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Events: the append-only log. This is the system of record. Never UPDATE/DELETE.
-- (seq, room_id) is unique and gives per-room total ordering for replay.
CREATE TABLE IF NOT EXISTS events (
    id          UUID PRIMARY KEY,
    room_id     UUID NOT NULL,
    seq         BIGINT NOT NULL,
    type        TEXT NOT NULL,
    player_id   UUID,
    payload     JSONB NOT NULL DEFAULT '{}',
    version     INT NOT NULL DEFAULT 1,
    timestamp   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (room_id, seq)
);

-- Replay/range scans hit (room_id, seq); keep it indexed (UNIQUE covers it).
CREATE INDEX IF NOT EXISTS idx_events_room_time ON events (room_id, timestamp);
