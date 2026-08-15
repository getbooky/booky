-- Existing-library import: every file found by a scan gets a row. Matched
-- files link to a book; the rest sit in review until accepted or ignored.

CREATE TABLE import_files (
    id          INTEGER PRIMARY KEY,
    library_id  INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    size        INTEGER NOT NULL DEFAULT 0,
    status      TEXT NOT NULL DEFAULT 'review'
                CHECK (status IN ('matched', 'review', 'ignored')),
    book_id     INTEGER REFERENCES books(id) ON DELETE SET NULL,
    guess_title TEXT NOT NULL DEFAULT '',
    guess_author TEXT NOT NULL DEFAULT '',
    confidence  REAL NOT NULL DEFAULT 0,
    scanned_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (library_id, path)
);

CREATE INDEX idx_import_files_status ON import_files(library_id, status);
