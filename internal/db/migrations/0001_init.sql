-- Core schema. Identity rule: every book converges on the triple
-- (goodreads_id, isbn13, hardcover_id); any one of them is enough to re-match.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE quality_profiles (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    -- ordered JSON array of allowed formats, best first, e.g. ["epub","azw3","mobi"]
    formats         TEXT NOT NULL,
    cutoff_format   TEXT NOT NULL,
    language        TEXT NOT NULL DEFAULT 'en',
    preferred_terms TEXT NOT NULL DEFAULT '',
    avoided_terms   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE libraries (
    id                 INTEGER PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE,
    root_path          TEXT NOT NULL UNIQUE,
    quality_profile_id INTEGER NOT NULL REFERENCES quality_profiles(id),
    -- per-library OPDS/KoReader credentials (password stored hashed)
    opds_username      TEXT NOT NULL UNIQUE,
    opds_password_hash TEXT NOT NULL,
    created_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE authors (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL,
    sort_name    TEXT NOT NULL,
    goodreads_id TEXT UNIQUE,
    hardcover_id TEXT UNIQUE,
    monitored    INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE series (
    id           INTEGER PRIMARY KEY,
    author_id    INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    goodreads_id TEXT,
    hardcover_id TEXT,
    monitored    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE books (
    id            INTEGER PRIMARY KEY,
    author_id     INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    series_id     INTEGER REFERENCES series(id) ON DELETE SET NULL,
    series_num    REAL,
    title         TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    language      TEXT NOT NULL DEFAULT '',
    publisher     TEXT NOT NULL DEFAULT '',
    release_date  TEXT,
    goodreads_id  TEXT UNIQUE,
    hardcover_id  TEXT UNIQUE,
    isbn13        TEXT,
    -- JSON object of per-field manual-edit locks, e.g. {"title":true}
    field_locks   TEXT NOT NULL DEFAULT '{}',
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE editions (
    id       INTEGER PRIMARY KEY,
    book_id  INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    isbn13   TEXT,
    isbn10   TEXT,
    asin     TEXT,
    format   TEXT NOT NULL DEFAULT '',
    year     INTEGER,
    -- editions matching box-set/collection heuristics are flagged, never shown as books
    excluded INTEGER NOT NULL DEFAULT 0
);

-- a book's presence in a library: monitored flag + the imported file, if any
CREATE TABLE library_books (
    id         INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    book_id    INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    monitored  INTEGER NOT NULL DEFAULT 1,
    file_path  TEXT,
    file_format TEXT,
    file_size  INTEGER,
    added_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (library_id, book_id)
);

CREATE TABLE watched_lists (
    id             INTEGER PRIMARY KEY,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('goodreads_rss', 'hardcover')),
    -- goodreads: user id + shelf; hardcover: list id (token lives in secrets)
    source_ref     TEXT NOT NULL,
    library_id     INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    monitor_scope  TEXT NOT NULL DEFAULT 'book' CHECK (monitor_scope IN ('book', 'series', 'author')),
    on_remove      TEXT NOT NULL DEFAULT 'nothing' CHECK (on_remove IN ('nothing', 'unmonitor', 'delete')),
    search_on_add  INTEGER NOT NULL DEFAULT 1,
    enabled        INTEGER NOT NULL DEFAULT 1,
    last_checked   TEXT,
    etag           TEXT
);

CREATE TABLE history (
    id         INTEGER PRIMARY KEY,
    book_id    INTEGER REFERENCES books(id) ON DELETE SET NULL,
    library_id INTEGER REFERENCES libraries(id) ON DELETE SET NULL,
    kind       TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE blocklist (
    id           INTEGER PRIMARY KEY,
    book_id      INTEGER REFERENCES books(id) ON DELETE CASCADE,
    release_name TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

-- single-row-per-key app settings (poll interval, naming scheme, provider order…)
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE INDEX idx_books_author ON books(author_id);
CREATE INDEX idx_books_series ON books(series_id);
CREATE INDEX idx_library_books_book ON library_books(book_id);
CREATE INDEX idx_history_created ON history(created_at);
