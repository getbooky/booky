-- Send to Kindle. Devices mirror KoReader's ownership model: each account
-- pairs its own, an admin sees all. Outgoing email is strictly per-account
-- (no server default) — a device always sends through its owner's SMTP
-- account, and an account without one has the feature off.

CREATE TABLE kindle_devices (
    id             INTEGER PRIMARY KEY,
    -- the account that paired the device; 0 would mean pre-auth, but the
    -- feature requires an owner's SMTP account so 0 never sends
    owner_user_id  INTEGER NOT NULL DEFAULT 0,
    name           TEXT NOT NULL,
    email          TEXT NOT NULL,
    -- JSON arrays of library ids, same shape as KoReader devices: which
    -- libraries the device draws from, and which of those auto-send new
    -- arrivals on import
    library_ids    TEXT NOT NULL DEFAULT '[]',
    auto_ids       TEXT NOT NULL DEFAULT '[]',
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    last_sent      TEXT
);

CREATE TABLE kindle_smtp (
    owner_user_id  INTEGER PRIMARY KEY,
    from_addr      TEXT NOT NULL,
    host           TEXT NOT NULL,
    port           INTEGER NOT NULL DEFAULT 587,
    -- 'starttls' | 'tls' (implicit) | 'none'
    security       TEXT NOT NULL DEFAULT 'starttls',
    username       TEXT NOT NULL DEFAULT '',
    -- AES-256-GCM ciphertext (nonce-prefixed); the key lives in its own file
    -- outside the database so backups of booky.db can't yield the password.
    -- NULL means no password stored (open relays).
    password_ct    BLOB,
    updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
);
