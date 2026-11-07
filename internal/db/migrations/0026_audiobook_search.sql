-- Audiobooks are searched for and grabbed like ebooks, with their own
-- profile and their own monitor switch. The switch is off for every book:
-- an install that always wanted the EPUB doesn't suddenly start fetching
-- audiobooks too.

-- One global audiobook profile, next to the ebook one. It lives apart from
-- quality_profiles so nothing that picks an ebook profile can pick it.
CREATE TABLE audiobook_profile (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    -- ordered JSON array, best first: m4b | mp3 | m4a
    formats         TEXT NOT NULL DEFAULT '["m4b","mp3","m4a"]',
    language        TEXT NOT NULL DEFAULT 'english',
    preferred_terms TEXT NOT NULL DEFAULT 'unabridged',
    -- abridged readings and dramatizations don't follow the book's text,
    -- so a read-along can't be lined up with them
    avoided_terms   TEXT NOT NULL DEFAULT 'abridged, dramatized, dramatised, full cast',
    -- 0 = no limit
    min_size_mb     INTEGER NOT NULL DEFAULT 5 CHECK (min_size_mb >= 0),
    max_size_mb     INTEGER NOT NULL DEFAULT 0 CHECK (max_size_mb >= 0)
);

-- start from the languages the ebook profile already accepts
INSERT INTO audiobook_profile (id, language)
VALUES (1, COALESCE((SELECT language FROM quality_profiles ORDER BY id LIMIT 1), 'english'));

ALTER TABLE library_books ADD COLUMN audio_monitored INTEGER NOT NULL DEFAULT 0;

-- what a queue row is fetching
ALTER TABLE queue ADD COLUMN kind TEXT NOT NULL DEFAULT 'ebook' CHECK (kind IN ('ebook', 'audiobook'));

-- audiobook searches keep their own once-a-day record, so searching for the
-- ebook doesn't use up the audiobook's turn (or the other way round)
CREATE TABLE audio_search_attempts (
    book_id       INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
    last_searched TEXT NOT NULL
);
