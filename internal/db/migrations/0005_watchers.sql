-- Watchers. Lists gain a per-list quality-profile override and a
-- visible last error; list_items remembers what each list has already routed
-- (the diff base for "new since last poll" and for on-remove handling);
-- search_attempts backs the release-day taper and backlog rate limiting.

ALTER TABLE watched_lists ADD COLUMN quality_profile_id INTEGER REFERENCES quality_profiles(id);
ALTER TABLE watched_lists ADD COLUMN last_error TEXT NOT NULL DEFAULT '';

CREATE TABLE list_items (
    id          INTEGER PRIMARY KEY,
    list_id     INTEGER NOT NULL REFERENCES watched_lists(id) ON DELETE CASCADE,
    -- provider-scoped identity, e.g. "gr:61431922" or "hc:12345"
    external_id TEXT NOT NULL,
    book_id     INTEGER REFERENCES books(id) ON DELETE SET NULL,
    title       TEXT NOT NULL DEFAULT '',
    first_seen  TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (list_id, external_id)
);

CREATE TABLE search_attempts (
    book_id       INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
    last_searched TEXT NOT NULL
);
