-- Monitoring meant three different things at three levels, and only the
-- innermost one was ever a real decision.
--
--   authors.monitored  — keep this person's bibliography syncing
--   series.monitored   — keep watching this series for new entries
--   library_books.monitored — "I want this book, go and get it"
--
-- Nobody wants the first two off: if an author or series is in a library at
-- all, you want to hear about their new books. So they stop being toggles and
-- become tracking state the app maintains itself. Shelving becomes an
-- explicit, library-targeted act instead of a side effect of flipping a global
-- flag — which is also what stops one account's click from reaching into
-- another account's library.

-- Which series someone deliberately shelved, and where. This is not the same
-- as "some library holds a book of this series": a library can end up with one
-- entry through a search or an import without anyone asking for the set. Only
-- a deliberate shelving opts a library into receiving the series' future
-- books, so announced-but-unreleased entries the overlay discovers stay on the
-- series page — catalog-only, and still prunable if they never materialize.
CREATE TABLE series_libraries (
    series_id  INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    added_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (series_id, library_id)
);

CREATE INDEX idx_series_libraries_series ON series_libraries(series_id);

-- Preserve what the old cascade was doing: a series whose global flag was on
-- had its new books attached to the libraries holding it, so record exactly
-- those pairs.
INSERT INTO series_libraries (series_id, library_id)
SELECT DISTINCT sr.id, lb.library_id
FROM series sr
JOIN books b ON b.series_id = sr.id
JOIN library_books lb ON lb.book_id = b.id
WHERE sr.monitored = 1;

-- Anything already in a library is tracked, matching the new rule. New rows
-- get this set for them when a book lands in a library.
UPDATE authors SET monitored = 1
WHERE EXISTS (SELECT 1 FROM books b JOIN library_books lb ON lb.book_id = b.id
              WHERE b.author_id = authors.id);

UPDATE series SET monitored = 1
WHERE EXISTS (SELECT 1 FROM books b JOIN library_books lb ON lb.book_id = b.id
              WHERE b.series_id = series.id);

-- authors.monitor_mode is vestigial from here on. It chose whether a newly
-- discovered book was attached to a library or left catalog-only, and the
-- "attach" side had to guess WHICH library — the guess that could drop books
-- onto a shelf the person didn't hold. New books are now always catalog-only:
-- they show up on the author page, and you add the ones you want. The column
-- stays (dropping it is a table rebuild, and it costs nothing to leave) but
-- nothing reads it.
UPDATE authors SET monitor_mode = 'selected';
