-- One library, shared through tags.
--
-- Until now every person got their own library: a root folder, an OPDS feed,
-- and a copy (usually a hardlink) of each book they wanted. From here on there
-- is exactly one library holding one file per book, and TAGS decide who sees
-- what. A tag is a named audience ("Kids", "Household"); an account sees a
-- book when they share at least one tag with it. Admins see everything, and a
-- book with no tags at all is admin-only — nothing reaches anyone by accident.
--
-- Every account also gets a personal tag (owner_user_id set): what they add
-- for themselves lands there, the way Reely's personal groups work.
--
-- The conversion keeps everyone's access exactly as it was: each library
-- becomes a tag OF THE SAME ID, each grant becomes a membership, and each
-- book carries the tags of every library it sat in. Because the ids carry
-- over, the JSON id lists on KoReader and Kindle devices already name the
-- right tags and need no rewrite.

CREATE TABLE tags (
    id            INTEGER PRIMARY KEY,
    name          TEXT NOT NULL COLLATE NOCASE,
    -- set for a personal tag: the one account it belongs to
    owner_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- shared tag names are unique; personal tags are named after their owner and
-- may coincide with a shared tag ("sam" the person, "Sam" the old library)
CREATE UNIQUE INDEX idx_tags_shared_name ON tags(name) WHERE owner_user_id IS NULL;
CREATE UNIQUE INDEX idx_tags_owner ON tags(owner_user_id) WHERE owner_user_id IS NOT NULL;

CREATE TABLE book_tags (
    book_id  INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    added_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (book_id, tag_id)
);
CREATE INDEX idx_book_tags_tag ON book_tags(tag_id);

CREATE TABLE user_tags (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tag_id  INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, tag_id)
);
CREATE INDEX idx_user_tags_tag ON user_tags(tag_id);

-- which tags a watched list's books are given as they arrive
CREATE TABLE list_tags (
    list_id INTEGER NOT NULL REFERENCES watched_lists(id) ON DELETE CASCADE,
    tag_id  INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (list_id, tag_id)
);

-- every book a Kindle device has been emailed: send-on-tag fires whenever a
-- book gains an auto-send tag, and this is what keeps it to once per device
CREATE TABLE kindle_sent (
    device_id INTEGER NOT NULL REFERENCES kindle_devices(id) ON DELETE CASCADE,
    book_id   INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    sent_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (device_id, book_id)
);

-- OPDS signs in per account now. It gets its own password, separate from the
-- web login: e-reader apps store it in the clear, so a leaked reader config
-- must never be a way into the account itself. Empty means OPDS is off.
ALTER TABLE users ADD COLUMN opds_password_hash TEXT NOT NULL DEFAULT '';

-- ---- carry access over ----

INSERT INTO tags (id, name) SELECT id, name FROM libraries;

INSERT INTO book_tags (book_id, tag_id)
SELECT DISTINCT book_id, library_id FROM library_books;

INSERT INTO user_tags (user_id, tag_id)
SELECT user_id, library_id FROM user_libraries;

INSERT INTO list_tags (list_id, tag_id)
SELECT id, library_id FROM watched_lists;

INSERT INTO tags (name, owner_user_id) SELECT username, id FROM users;
INSERT INTO user_tags (user_id, tag_id) SELECT owner_user_id, id FROM tags WHERE owner_user_id IS NOT NULL;

-- Nothing already on a Kindle gets emailed again: every book a device would
-- auto-send today counts as sent.
INSERT OR IGNORE INTO kindle_sent (device_id, book_id)
SELECT DISTINCT d.id, lb.book_id
FROM kindle_devices d, json_each(d.auto_ids) a
JOIN library_books lb ON lb.library_id = a.value
WHERE lb.file_path IS NOT NULL AND lb.file_path != '';

-- ---- collapse to one library ----
-- The lowest-id library survives as THE library (its root folder, profile
-- and naming carry on). Every other membership row folds into it: the kept
-- row per book is the one with a file, preferring the survivor's own copy.
-- Files of the folded rows are recorded so the startup consolidation can
-- move the kept file into the survivor's root and set any differing
-- duplicate aside — SQL can't touch the disk, and nothing is ever deleted
-- here.

CREATE TABLE library_merge (
    book_id     INTEGER NOT NULL,
    file_path   TEXT NOT NULL,
    file_format TEXT,
    file_size   INTEGER,
    done        INTEGER NOT NULL DEFAULT 0
);

CREATE TEMP TABLE keep AS
SELECT book_id, (
    SELECT x.id FROM library_books x
    WHERE x.book_id = lb.book_id
    ORDER BY (x.file_path IS NOT NULL AND x.file_path != '') DESC,
             x.library_id = (SELECT MIN(id) FROM libraries) DESC,
             x.id
    LIMIT 1) AS row_id,
    MAX(lb.monitored) AS monitored
FROM library_books lb GROUP BY lb.book_id;

INSERT INTO library_merge (book_id, file_path, file_format, file_size)
SELECT lb.book_id, lb.file_path, lb.file_format, lb.file_size
FROM library_books lb JOIN keep k ON k.book_id = lb.book_id
WHERE lb.id != k.row_id AND lb.file_path IS NOT NULL AND lb.file_path != '';

DELETE FROM library_books WHERE id NOT IN (SELECT row_id FROM keep);

UPDATE library_books SET
    library_id = (SELECT MIN(id) FROM libraries),
    monitored  = (SELECT k.monitored FROM keep k WHERE k.row_id = library_books.id);

DROP TABLE keep;

-- everything else that pointed at a folded library now points at the one
UPDATE queue SET library_id = (SELECT MIN(id) FROM libraries);
UPDATE history SET library_id = (SELECT MIN(id) FROM libraries) WHERE library_id IS NOT NULL;
UPDATE watched_lists SET library_id = (SELECT MIN(id) FROM libraries);
DELETE FROM import_files WHERE library_id != (SELECT MIN(id) FROM libraries)
    AND path IN (SELECT path FROM import_files WHERE library_id = (SELECT MIN(id) FROM libraries));
UPDATE import_files SET library_id = (SELECT MIN(id) FROM libraries);
INSERT OR IGNORE INTO series_libraries (series_id, library_id, added_at)
SELECT series_id, (SELECT MIN(id) FROM libraries), MIN(added_at) FROM series_libraries GROUP BY series_id;
DELETE FROM series_libraries WHERE library_id != (SELECT MIN(id) FROM libraries);

-- per-library OPDS logins go away with the libraries; the survivor's is
-- disabled so its old credentials stop working on upgrade day
UPDATE libraries SET opds_password_hash = 'unset';
DELETE FROM libraries WHERE id != (SELECT MIN(id) FROM libraries);
UPDATE libraries SET name = 'Library';

DROP TABLE user_libraries;
