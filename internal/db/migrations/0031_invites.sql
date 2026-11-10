-- Invite links: an admin hands out a one-time link, and whoever opens it
-- picks their own username and password. Only the token's hash is stored,
-- so the link can't be rebuilt from the database.
CREATE TABLE invites (
    id         INTEGER PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    -- who the link is for, in the admin's words
    note       TEXT NOT NULL DEFAULT '',
    -- shared tags the new account joins (JSON array of tag ids)
    tag_ids    TEXT NOT NULL DEFAULT '[]',
    -- usernames, not ids: account ids are reused after a delete
    created_by TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL,
    used_at    TEXT,
    used_by    TEXT NOT NULL DEFAULT ''
);
