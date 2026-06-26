-- State snapshots: a materialized fold of a room's state at a given seq, so
-- rehydration (reconnect / crash recovery) replays only the tail of the event
-- log instead of the whole match. One latest snapshot per room. Also created at
-- boot via EnsureSchema for already-initialized databases.
CREATE TABLE IF NOT EXISTS snapshots (
    room_id    UUID PRIMARY KEY,
    seq        BIGINT NOT NULL,
    state      JSONB  NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
