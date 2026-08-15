package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// editableFields maps API field names to columns for manual metadata edits.
// genres travels as a comma-separated string in the API and is stored as a
// JSON array. seriesName/seriesNum are also editable but resolve through the
// series relation (handled separately below) under the shared "series" lock.
var editableFields = map[string]string{
	"title":       "title",
	"description": "description",
	"language":    "language",
	"publisher":   "publisher",
	"releaseDate": "release_date",
	"isbn13":      "isbn13",
	"genres":      "genres",
	// The canonical Hardcover identity is hand-editable so a wrong adoption
	// is recoverable: paste the right id (refresh then pulls that exact
	// book), or clear it (the next refresh re-matches by title/author —
	// unless the cleared field is locked, which stops adoption entirely).
	"hardcoverId": "hardcover_id",
}

// lockKey maps an edited field to its refresh-protection lock name.
func lockKey(field string) string {
	if field == "seriesName" || field == "seriesNum" {
		return "series"
	}
	return field
}

// relationFields are editable but live in another table rather than a books
// column, so EditBook resolves them before the row update.
var relationFields = map[string]bool{"author": true, "seriesName": true, "seriesNum": true}

// moveToAuthorTx repoints a book at the named author, find-or-creating that
// author, and returns the new author id. A book carrying a series moves with
// it: series rows belong to an author, so leaving the old one behind would
// file the book under a series on someone else's page.
func (s *Store) moveToAuthor(bookID int64, name string) (int64, error) {
	authorID, err := s.upsertAuthor(name)
	if err != nil {
		return 0, err
	}
	var seriesName string
	if err := s.db.QueryRow(`SELECT COALESCE(se.name, '') FROM books b
		LEFT JOIN series se ON se.id = b.series_id WHERE b.id = ?`, bookID).Scan(&seriesName); err != nil {
		return 0, fmt.Errorf("book %d: %w", bookID, err)
	}
	if seriesName != "" {
		sid, err := s.upsertSeries(authorID, seriesName)
		if err != nil {
			return 0, err
		}
		if _, err := s.db.Exec(`UPDATE books SET series_id = ? WHERE id = ?`, sid, bookID); err != nil {
			return 0, err
		}
	}
	if _, err := s.db.Exec(`UPDATE books SET author_id = ? WHERE id = ?`, authorID, bookID); err != nil {
		return 0, fmt.Errorf("move book %d to %q: %w", bookID, name, err)
	}
	return authorID, nil
}

// EditBook applies manual metadata edits. Edited fields are locked so
// provider refreshes never overwrite them (per-field, as designed).
// Series edits resolve the relation: a new name find-or-creates a series
// under the book's author, an empty name detaches the book from its series.
func (s *Store) EditBook(bookID int64, updates map[string]string, lock bool) error {
	if len(updates) == 0 {
		return nil
	}
	for field := range updates {
		if _, ok := editableFields[field]; !ok && !relationFields[field] {
			return fmt.Errorf("field %q is not editable", field)
		}
	}

	// The author moves first: the series relation below resolves under
	// whichever author the book ends up on.
	if name, ok := updates["author"]; ok {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("author cannot be empty")
		}
		if _, err := s.moveToAuthor(bookID, strings.TrimSpace(name)); err != nil {
			return err
		}
	}

	seriesName, hasName := updates["seriesName"]
	seriesName = strings.TrimSpace(seriesName)
	numStr, hasNum := updates["seriesNum"]
	var seriesNum any // bound as series_num when set
	if hasNum {
		if v := strings.TrimSpace(numStr); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("series number must be numeric, got %q", numStr)
			}
			seriesNum = f
		}
	}
	// Resolve the series relation before the row update.
	setSeriesID := false
	var seriesIDVal any
	if hasName {
		setSeriesID = true
		if seriesName != "" {
			var authorID int64
			if err := s.db.QueryRow(`SELECT author_id FROM books WHERE id = ?`, bookID).Scan(&authorID); err != nil {
				return fmt.Errorf("book %d: %w", bookID, err)
			}
			sid, err := s.upsertSeries(authorID, seriesName)
			if err != nil {
				return err
			}
			seriesIDVal = sid
		} else {
			// detaching also clears the position, whatever was passed
			seriesNum = nil
			hasNum = true
		}
	}

	locks, err := s.fieldLocks(bookID)
	if err != nil {
		return err
	}
	if lock {
		for field := range updates {
			locks[lockKey(field)] = true
		}
	}
	rawLocks, err := json.Marshal(locks)
	if err != nil {
		return err
	}
	has := func(field string) bool { _, ok := updates[field]; return ok }
	// Static update: each column is overwritten only when the caller supplied
	// that field, so there's no dynamic SQL and every value is bound.
	_, err = s.db.Exec(`UPDATE books SET
		title        = CASE WHEN ? THEN ? ELSE title END,
		description  = CASE WHEN ? THEN ? ELSE description END,
		language     = CASE WHEN ? THEN ? ELSE language END,
		publisher    = CASE WHEN ? THEN ? ELSE publisher END,
		release_date = CASE WHEN ? THEN ? ELSE release_date END,
		isbn13       = CASE WHEN ? THEN ? ELSE isbn13 END,
		hardcover_id = CASE WHEN ? THEN ? ELSE hardcover_id END,
		genres       = CASE WHEN ? THEN ? ELSE genres END,
		series_id    = CASE WHEN ? THEN ? ELSE series_id END,
		series_num   = CASE WHEN ? THEN ? ELSE series_num END,
		field_locks  = ?
		WHERE id = ?`,
		has("title"), updates["title"],
		has("description"), updates["description"],
		has("language"), updates["language"],
		has("publisher"), updates["publisher"],
		has("releaseDate"), nullStr(updates["releaseDate"]),
		has("isbn13"), nullStr(strings.TrimSpace(updates["isbn13"])),
		// empty clears to NULL — the column is UNIQUE, and "" on many rows
		// would collide where NULL never does
		has("hardcoverId"), nullStr(strings.TrimSpace(updates["hardcoverId"])),
		has("genres"), genresJSON(splitGenres(updates["genres"])),
		setSeriesID, seriesIDVal,
		hasNum, seriesNum,
		string(rawLocks),
		bookID)
	return err
}

// splitGenres parses the API's comma-separated genres value.
func splitGenres(s string) []string {
	var out []string
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// SetFieldLock toggles a single field's refresh-protection lock.
func (s *Store) SetFieldLock(bookID int64, field string, locked bool) error {
	if _, ok := editableFields[field]; !ok && field != "series" && field != "cover" && field != "author" {
		return fmt.Errorf("field %q is not lockable", field)
	}
	locks, err := s.fieldLocks(bookID)
	if err != nil {
		return err
	}
	if locked {
		locks[field] = true
	} else {
		delete(locks, field)
	}
	raw, err := json.Marshal(locks)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE books SET field_locks = ? WHERE id = ?`, string(raw), bookID)
	return err
}

// MoveBook moves a book's library membership. Books with imported files
// refuse to move until the importer can relocate files safely.
func (s *Store) MoveBook(bookID, fromLibraryID, toLibraryID int64) error {
	var filePath sql.NullString
	err := s.db.QueryRow(`SELECT file_path FROM library_books WHERE library_id = ? AND book_id = ?`,
		fromLibraryID, bookID).Scan(&filePath)
	if err != nil {
		return fmt.Errorf("book is not in that library")
	}
	if filePath.Valid && filePath.String != "" {
		return fmt.Errorf("this book has a file on disk — moving books with files between libraries isn't supported yet")
	}
	_, err = s.db.Exec(`UPDATE library_books SET library_id = ? WHERE library_id = ? AND book_id = ?`,
		toLibraryID, fromLibraryID, bookID)
	return err
}

// RemoveBookFromLibrary removes a book's membership. mode: "library" keeps
// the file on disk, "file" deletes this library's copy, "block" deletes the
// copy and blocklists the book so watched lists can't re-add it.
//
// The book ROW always survives: removal demotes the book to catalog-only, so
// it stays visible (unmonitored) on its author and series pages and one
// monitor toggle brings it back. Pruning it would be an illusion anyway —
// the weekly bibliography sync re-creates catalog rows for everything the
// author wrote. Only deleting the author removes books from the catalog.
func (s *Store) RemoveBookFromLibrary(bookID, libraryID int64, mode string) error {
	var filePath sql.NullString
	err := s.db.QueryRow(`SELECT file_path FROM library_books WHERE library_id = ? AND book_id = ?`,
		libraryID, bookID).Scan(&filePath)
	if err != nil {
		return fmt.Errorf("book is not in that library")
	}
	if mode != "library" && filePath.Valid && filePath.String != "" {
		// hard-linked copies in other libraries keep their own directory entry;
		// removing this path only drops this library's link
		if err := os.Remove(filePath.String); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete file: %w", err)
		}
	}
	if mode == "block" {
		var title string
		if err := s.db.QueryRow(`SELECT title FROM books WHERE id = ?`, bookID).Scan(&title); err == nil {
			if _, err := s.db.Exec(`INSERT INTO blocklist (book_id, release_name, source, reason)
				VALUES (?, ?, 'user', 'removed and blocked from re-import')`, bookID, title); err != nil {
				return err
			}
		}
	}
	if _, err := s.db.Exec(`DELETE FROM library_books WHERE library_id = ? AND book_id = ?`,
		libraryID, bookID); err != nil {
		return err
	}
	// An author or series with nothing left in any library stops being tracked
	// — no point syncing a bibliography nobody holds. A hand-added author is
	// exempt: browsing their catalog-only bibliography is exactly what they're
	// for. Neither is a visibility rule (ListAuthors and ListSeries decide
	// that from the books), just an end to pointless provider traffic.
	if _, err := s.db.Exec(`UPDATE authors SET monitored = 0
		WHERE id = (SELECT author_id FROM books WHERE id = ?)
		  AND added_manually = 0
		  AND NOT EXISTS (
			SELECT 1 FROM books b2 JOIN library_books lb2 ON lb2.book_id = b2.id
			WHERE b2.author_id = (SELECT author_id FROM books WHERE id = ?))`,
		bookID, bookID); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE series SET monitored = 0
		WHERE id = (SELECT series_id FROM books WHERE id = ?)
		  AND NOT EXISTS (
			SELECT 1 FROM books b2 JOIN library_books lb2 ON lb2.book_id = b2.id
			WHERE b2.series_id = (SELECT series_id FROM books WHERE id = ?))`,
		bookID, bookID); err != nil {
		return err
	}
	// Removing the last of a series from a library withdraws that library's
	// standing request for it. Otherwise the next entry to be announced would
	// refill a shelf somebody had just deliberately cleared.
	_, err = s.db.Exec(`DELETE FROM series_libraries
		WHERE library_id = ?
		  AND series_id = (SELECT series_id FROM books WHERE id = ?)
		  AND NOT EXISTS (
			SELECT 1 FROM library_books lb2 JOIN books b2 ON b2.id = lb2.book_id
			WHERE lb2.library_id = ? AND b2.series_id = (SELECT series_id FROM books WHERE id = ?))`,
		libraryID, bookID, libraryID, bookID)
	return err
}

// LibraryState reports whether a book is attached to a library, and whether
// that membership holds a file. Both false when there's no membership.
func (s *Store) LibraryState(bookID, libraryID int64) (attached, onShelf bool, err error) {
	var fp sql.NullString
	err = s.db.QueryRow(`SELECT file_path FROM library_books WHERE library_id = ? AND book_id = ?`,
		libraryID, bookID).Scan(&fp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, fp.Valid && fp.String != "", nil
}

// dropBookCover removes the cached cover with its book row; a reused id must
// never inherit the predecessor's image.
func (s *Store) dropBookCover(bookID int64) {
	if s.Covers != nil {
		_ = s.Covers.Remove(bookID)
	}
}

// AuthorSummary returns an author's name and book count. Callers that delete
// need it BEFORE the cascade — afterwards there is nothing left to name the
// author with, and a history line reading "author 12 deleted" is no record
// at all. A missing author comes back as an empty name.
func (s *Store) AuthorSummary(authorID int64) (name string, books int) {
	_ = s.db.QueryRow(`SELECT a.name, (SELECT COUNT(*) FROM books WHERE author_id = a.id)
		FROM authors a WHERE a.id = ?`, authorID).Scan(&name, &books)
	return name, books
}

// LibraryName returns a library's name, empty when it's gone. Same purpose as
// AuthorSummary: naming a thing in history that is about to stop existing.
func (s *Store) LibraryName(libraryID int64) (name string) {
	_ = s.db.QueryRow(`SELECT name FROM libraries WHERE id = ?`, libraryID).Scan(&name)
	return name
}

// DeleteAuthor removes an author and (via cascade) their books, series and
// library memberships from the catalog. Files on disk are untouched.
func (s *Store) DeleteAuthor(authorID int64) error {
	// book ids must be read before the cascade erases them — their cached
	// covers go with the rows
	var bookIDs []int64
	rows, err := s.db.Query(`SELECT id FROM books WHERE author_id = ?`, authorID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		bookIDs = append(bookIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM authors WHERE id = ?`, authorID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("author %d not found", authorID)
	}
	for _, id := range bookIDs {
		s.dropBookCover(id)
	}
	if s.AuthorPhotos != nil {
		_ = s.AuthorPhotos.Remove(authorID)
	}
	return nil
}

// DeleteAuthorFiles deletes every file on disk belonging to the author's
// books, then prunes the directories those deletions emptied (up to each
// library root) — with the "{Author}/{Title}" naming scheme the author's
// folder disappears with its last book. Only empty directories are removed,
// so an import-in-place folder still holding anything else survives. Call
// BEFORE DeleteAuthor: the cascade erases the file paths. Returns how many
// files were deleted.
func (s *Store) DeleteAuthorFiles(authorID int64) (int, error) {
	rows, err := s.db.Query(`SELECT lb.file_path, l.root_path
		FROM library_books lb
		JOIN books b ON b.id = lb.book_id
		JOIN libraries l ON l.id = lb.library_id
		WHERE b.author_id = ? AND lb.file_path IS NOT NULL AND lb.file_path != ''`, authorID)
	if err != nil {
		return 0, err
	}
	return removeFileRows(rows)
}

// removeFileRows deletes each (file_path, root_path) row's file and prunes
// the directories that emptied — shared by the author- and library-wide
// file deletes. Closes rows.
func removeFileRows(rows *sql.Rows) (int, error) {
	type target struct{ path, root string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.path, &t.root); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	deleted := 0
	for _, t := range targets {
		if err := os.Remove(t.path); err != nil && !os.IsNotExist(err) {
			return deleted, fmt.Errorf("delete %s: %w", t.path, err)
		}
		deleted++
		pruneEmptyDirs(filepath.Dir(t.path), t.root)
	}
	return deleted, nil
}

// DeleteLibraryFiles deletes every file this library holds on disk, pruning
// the directories those deletions emptied (up to the library root). Call
// BEFORE DeleteLibrary: the cascade erases the membership rows holding the
// paths. Returns how many files were deleted.
func (s *Store) DeleteLibraryFiles(libraryID int64) (int, error) {
	rows, err := s.db.Query(`SELECT lb.file_path, l.root_path
		FROM library_books lb
		JOIN libraries l ON l.id = lb.library_id
		WHERE lb.library_id = ? AND lb.file_path IS NOT NULL AND lb.file_path != ''`, libraryID)
	if err != nil {
		return 0, err
	}
	return removeFileRows(rows)
}

// DeleteLibrary removes a library row. Memberships, pending imports and
// queue rows cascade away; history keeps its rows with the library reference
// nulled. Books demote to catalog-only — they leave the Library view but
// stay visible on their author and series pages, like a single-book remove.
func (s *Store) DeleteLibrary(libraryID int64) error {
	res, err := s.db.Exec(`DELETE FROM libraries WHERE id = ?`, libraryID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("library %d not found", libraryID)
	}
	return nil
}

// pruneEmptyDirs removes dir and then its parents while each is empty and
// strictly inside root. Stops silently at the first non-empty directory.
func pruneEmptyDirs(dir, root string) {
	root = filepath.Clean(root)
	sep := string(os.PathSeparator)
	for {
		dir = filepath.Clean(dir)
		if dir == root || dir == "/" || dir == "." || !strings.HasPrefix(dir+sep, root+sep) {
			return
		}
		if err := os.Remove(dir); err != nil {
			return // not empty (or already gone) — never force
		}
		dir = filepath.Dir(dir)
	}
}

// SetLibraryProfile assigns a quality profile to a library.
func (s *Store) SetLibraryProfile(libraryID, profileID int64) error {
	res, err := s.db.Exec(`UPDATE libraries SET quality_profile_id = ? WHERE id = ?`, profileID, libraryID)
	if err != nil {
		return fmt.Errorf("assign profile: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("library %d not found", libraryID)
	}
	return nil
}

// AnyFilePath returns some library copy's file path for the book, or ""
// when no copy is on disk.
func (s *Store) AnyFilePath(bookID int64) string {
	var p string
	_ = s.db.QueryRow(`SELECT file_path FROM library_books
		WHERE book_id = ? AND file_path IS NOT NULL AND file_path != '' LIMIT 1`, bookID).Scan(&p)
	return p
}

func (s *Store) SetAuthorMonitored(authorID int64, monitored bool) error {
	_, err := s.db.Exec(`UPDATE authors SET monitored = ? WHERE id = ?`, monitored, authorID)
	return err
}

// MarkSeriesTracked records that a series is worth watching for new entries.
// It is not a user decision: a series earns it by having a book in a library,
// and nothing turns it back off.
//
// The toggle this replaces also cascaded a monitored flag across every copy in
// every library, which is how one account's click could start downloads into
// another account's shelf.
func (s *Store) MarkSeriesTracked(seriesID int64) error {
	_, err := s.db.Exec(`UPDATE series SET monitored = 1 WHERE id = ?`, seriesID)
	return err
}

// AddSeriesToLibrary shelves a whole series in ONE library: every book of it
// joins that library monitored, so the acquisition engine goes after the ones
// that are missing. Books already there are re-monitored; copies in OTHER
// libraries are left completely alone, which is the point of naming the
// library instead of flipping a global flag and hoping.
//
// Returns how many books the library gained.
func (s *Store) AddSeriesToLibrary(seriesID, libraryID int64) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	var gained int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM books
		WHERE series_id = ?
		  AND id NOT IN (SELECT book_id FROM library_books WHERE library_id = ?)`,
		seriesID, libraryID).Scan(&gained); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO library_books (library_id, book_id, monitored)
		SELECT ?, id, 1 FROM books WHERE series_id = ?
		ON CONFLICT(library_id, book_id) DO UPDATE SET monitored = 1`,
		libraryID, seriesID); err != nil {
		return 0, err
	}
	// record the opt-in, so future entries in this series know to join THIS
	// library — and only libraries somebody actually asked for
	if _, err := tx.Exec(`INSERT INTO series_libraries (series_id, library_id) VALUES (?, ?)
		ON CONFLICT(series_id, library_id) DO NOTHING`, seriesID, libraryID); err != nil {
		return 0, err
	}
	// it is in a library now, so both it and its author are tracked
	if _, err := tx.Exec(`UPDATE series SET monitored = 1 WHERE id = ?`, seriesID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE authors SET monitored = 1
		WHERE id = (SELECT author_id FROM series WHERE id = ?)`, seriesID); err != nil {
		return 0, err
	}
	return gained, tx.Commit()
}

// AttachMonitoredSeriesBooks catches new entries up with the series they
// belong to, after a bibliography sync: a book that just appeared joins every
// library that was deliberately given the series, monitored, like the rest.
//
// It reads series_libraries rather than inferring a target. The old version
// guessed "the library holding most of this author's books, else the first
// library" — a coin flip for a series two people had shelved, and possibly a
// library the person couldn't see. Inferring from library_books instead would
// be just as wrong the other way: a library that picked up one entry through a
// search never asked for the set, and auto-shelving there would also make the
// overlay's announced-but-unreleased finds permanent, since the stale prune
// only reclaims books that are in no library.
//
// Returns how many memberships were created.
func (s *Store) AttachMonitoredSeriesBooks(authorID int64) (int, error) {
	res, err := s.db.Exec(`
		INSERT INTO library_books (library_id, book_id, monitored)
		SELECT sl.library_id, b.id, 1
		FROM books b
		JOIN series sr ON sr.id = b.series_id
		JOIN series_libraries sl ON sl.series_id = b.series_id
		WHERE sr.author_id = ?
		ON CONFLICT(library_id, book_id) DO NOTHING`, authorID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
