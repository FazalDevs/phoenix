-- Leaderboard read model (CQRS projection). Built by folding MatchEnded events
-- from the event log; see internal/leaderboard. Also created at boot via
-- EnsureSchema so it works on already-initialized databases.
CREATE TABLE IF NOT EXISTS leaderboard (
    player_id UUID PRIMARY KEY,
    wins   INT NOT NULL DEFAULT 0,
    losses INT NOT NULL DEFAULT 0,
    draws  INT NOT NULL DEFAULT 0
);
