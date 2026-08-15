-- Delivery. KoReader devices carry a bearer token the built
-- plugin authenticates with; sessions back the web UI's cookie auth.

CREATE TABLE devices (
    id             INTEGER PRIMARY KEY,
    name           TEXT NOT NULL,
    token          TEXT NOT NULL UNIQUE,
    -- JSON arrays of library ids: which the device may browse/download, and
    -- which of those auto-download new arrivals on check-in
    library_ids    TEXT NOT NULL DEFAULT '[]',
    auto_ids       TEXT NOT NULL DEFAULT '[]',
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    last_sync      TEXT
);

CREATE TABLE sessions (
    token      TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
);

CREATE INDEX idx_sessions_expiry ON sessions(expires_at);
