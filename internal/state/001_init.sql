-- Per-user orchestration state for traffic-limiter.
CREATE TABLE IF NOT EXISTS user_state (
    user_uuid              TEXT PRIMARY KEY,
    wl_state               TEXT NOT NULL DEFAULT 'active',   -- active | grace | blocked
    wl_grace_until         INTEGER,                          -- unix seconds
    wl_original_limit      INTEGER,                          -- saved data_limit_bytes before Plan-B override
    wl_original_strategy   TEXT,                             -- saved traffic_limit_strategy
    wl_over_limit          INTEGER,                          -- data_limit_bytes at which grace started
    basic_used_bytes       INTEGER NOT NULL DEFAULT 0,
    basic_limit_bytes      INTEGER NOT NULL DEFAULT 0,
    basic_state            TEXT NOT NULL DEFAULT 'active',   -- active | blocked
    last_wl_limited_at     INTEGER,
    last_basic_limited_at  INTEGER,
    last_reconciled_at     INTEGER,
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL
);

-- High-water marks for per-node per-user usage, so we only count deltas.
CREATE TABLE IF NOT EXISTS usage_checkpoint (
    node_uuid    TEXT NOT NULL,
    user_uuid    TEXT NOT NULL,
    bytes_total  INTEGER NOT NULL,
    fetched_at   INTEGER NOT NULL,
    PRIMARY KEY (node_uuid, user_uuid)
);

CREATE INDEX IF NOT EXISTS idx_user_state_wl  ON user_state(wl_state);
CREATE INDEX IF NOT EXISTS idx_user_state_bas ON user_state(basic_state);
