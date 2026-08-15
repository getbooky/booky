-- Per-user reading position for the in-app web reader. user_id 0 is the
-- pre-auth (no accounts yet) reader; locator is the engine's position string
-- (an EPUB CFI for foliate-js), percent a 0..1 fraction for shelf badges.
CREATE TABLE reading_progress (
    user_id    INTEGER NOT NULL DEFAULT 0,
    book_id    INTEGER NOT NULL,
    locator    TEXT    NOT NULL DEFAULT '',
    percent    REAL    NOT NULL DEFAULT 0,
    updated_at TEXT    NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, book_id)
);
