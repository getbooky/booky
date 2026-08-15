-- The download queue: one row per grab, from queued through imported.
CREATE TABLE queue (
    id            INTEGER PRIMARY KEY,
    book_id       INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    library_id    INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    release_title TEXT NOT NULL,
    source        TEXT NOT NULL,
    protocol      TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'queued', -- queued|downloading|importing|done|failed
    external_id   TEXT NOT NULL DEFAULT '',       -- SABnzbd job id, or local path for direct
    detail        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_queue_status ON queue(status);
