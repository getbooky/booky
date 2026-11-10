-- Invite links: an admin hands out a one-time link, and whoever opens it
-- picks their own username and password. Only the token's hash is stored,
-- so the link can't be rebuilt from the database.
CREATE TABLE invites (
    id         INTEGER PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    -- who the link is for, in the admin's words
    note       TEXT NOT NULL DEFAULT '',
    -- usernames, not ids: account ids are reused after a delete
    created_by TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT,
    used_by    TEXT NOT NULL DEFAULT ''
);

-- the shared tags a link's account joins. Rows go with their tag: tag ids
-- are reused, so a link must never outlive a tag and then hand out whatever
-- tag is created next under the same id.
CREATE TABLE invite_tags (
    invite_id INTEGER NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
    tag_id    INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (invite_id, tag_id)
);
