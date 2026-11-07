-- Audiobooks, and the read-alongs built from them.
--
-- A book has at most one audiobook: a single file sitting next to the ebook
-- under the same name (Title.epub, Title.m4b). Who can hear it is whoever can
-- read the book — tags decide, as for every file.
--
-- status follows the read-along build: queued → working → ready, or failed
-- with error saying why. The build's output (chunked audio, the read-along
-- EPUB, the timing file) lives under the config directory, never in the
-- library: it can always be rebuilt from the two files.
CREATE TABLE audiobooks (
    book_id    INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
    file_path  TEXT    NOT NULL,
    -- the container, sniffed from the file's bytes: 'mp3', or 'mov' for
    -- m4b/m4a (ffmpeg's demuxer names, passed to it verbatim)
    format     TEXT    NOT NULL CHECK (format IN ('mp3', 'mov')),
    size       INTEGER NOT NULL DEFAULT 0,
    duration   REAL    NOT NULL DEFAULT 0,
    added_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    status     TEXT    NOT NULL DEFAULT 'queued'
               CHECK (status IN ('queued', 'working', 'ready', 'failed')),
    phase      TEXT    NOT NULL DEFAULT '',
    progress   REAL    NOT NULL DEFAULT 0,
    error      TEXT    NOT NULL DEFAULT '',
    -- share of the narration that matched the text (readalong.Stats.Match)
    match      REAL    NOT NULL DEFAULT 0,
    -- bumped whenever the file changes: a build only records its result if
    -- the audiobook is still the one it started on
    generation INTEGER NOT NULL DEFAULT 1,
    queued_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    built_at   TEXT
);

CREATE INDEX idx_audiobooks_status ON audiobooks(status, queued_at);

-- Per-account listening position, in seconds from the start of the
-- audiobook — what lets a phone and an e-reader hand a book back and forth.
-- user_id 0 is the pre-auth listener, as for reading_progress.
CREATE TABLE listen_progress (
    user_id    INTEGER NOT NULL DEFAULT 0,
    book_id    INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    position   REAL    NOT NULL DEFAULT 0,
    updated_at TEXT    NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, book_id)
);
