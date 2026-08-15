// Package catalog owns library state: authors, series, books, editions and
// each book's presence in a library. All writes funnel through UpsertBook so
// identity stays converged on (goodreads_id, isbn13, hardcover_id).
package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/getbooky/booky/internal/metadata"
)

// isForeignKeyErr reports whether err is SQLite's foreign-key constraint
// violation, however the driver phrases it.
func isForeignKeyErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key constraint")
}

type Store struct {
	db *sql.DB
	// Covers and AuthorPhotos, when set, are the on-disk image caches keyed
	// by row id. Row ids are reused after deletion (plain SQLite rowids), so
	// every delete path must drop the cached file with the row — a surviving
	// file would silently become the next occupant's image.
	Covers       *CoverCache
	AuthorPhotos *CoverCache
	// Exclude, when set, supplies the user-editable exclusion terms (the
	// compilation keywords live there) — used to refuse minting a series
	// whose NAME reads as a box set.
	Exclude func() []string
}

func (s *Store) excludeTerms() []string {
	if s.Exclude != nil {
		return s.Exclude()
	}
	return metadata.DefaultExcludeTerms
}

func New(db *sql.DB) *Store { return &Store{db: db} }

// Visibility narrows a listing (or a cascade's write target) to one account.
// A nil *Visibility means no narrowing at all — admins, background loops, and
// the pre-auth wizard — so every caller that doesn't care passes nil.
type Visibility struct {
	// LibraryIDs is the set of libraries the account may reach.
	LibraryIDs []int64
	// UserID keeps authors the account added by hand visible before any of
	// their books have been shelved anywhere (see migration 0018).
	UserID int64
}

func (v *Visibility) libraryIDs() []int64 {
	if v == nil {
		return nil
	}
	return v.LibraryIDs
}

func (v *Visibility) userID() int64 {
	if v == nil {
		return 0
	}
	return v.UserID
}

// unscoped is the bound flag the queries below switch on: 1 disables every
// narrowing clause, keeping one query for both cases instead of two that can
// drift apart.
func (v *Visibility) unscoped() int64 {
	if v == nil {
		return 1
	}
	return 0
}

// idList renders a placeholder list for an id set. Only the NUMBER of
// placeholders varies with the input — every value stays a bound parameter,
// so there is no dynamic SQL in the injectable sense. The -1 sentinel keeps
// the list non-empty, since `IN ()` is a syntax error and an account with no
// libraries at all is a perfectly ordinary state.
func idList(ids []int64) (placeholders string, args []any) {
	args = make([]any, 0, len(ids)+1)
	args = append(args, int64(-1))
	for _, id := range ids {
		args = append(args, id)
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(args)), ","), args
}

// IDPlaceholders and UnscopedFlag expose the two pieces a Visibility-aware
// query needs, for the watcher's own "where do these books go" lookups. Bind
// UnscopedFlag first, then spread the args — same order as the SQL reads.
func IDPlaceholders(v *Visibility) (placeholders string, args []any) {
	return idList(v.libraryIDs())
}

func UnscopedFlag(v *Visibility) int64 { return v.unscoped() }

// AddAuthorFor records that a user added this author by hand, so they stay on
// that user's Authors page until one of the books lands in a library.
func (s *Store) AddAuthorFor(userID, authorID int64) error {
	if userID == 0 {
		return nil // admin or pre-auth: they see every author anyway
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO user_authors (user_id, author_id) VALUES (?, ?)`,
		userID, authorID)
	return err
}

type Author struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SortName  string `json:"sortName"`
	Monitored bool   `json:"monitored"`
	BookCount int    `json:"bookCount"`
	OnShelf   int    `json:"onShelf"`
	// presentation data synced from Hardcover; the photo itself is served
	// from the on-disk cache via /authors/{id}/photo
	Bio      string `json:"bio,omitempty"`
	HasPhoto bool   `json:"hasPhoto,omitempty"`
}

type Series struct {
	ID        int64  `json:"id"`
	AuthorID  int64  `json:"authorId"`
	Author    string `json:"author"`
	Name      string `json:"name"`
	Monitored bool   `json:"monitored"`
	Total     int    `json:"total"`
	OnShelf   int    `json:"onShelf"`
	// first few in-order book ids, for the index page's cover fan
	CoverBookIDs []int64 `json:"coverBookIds,omitempty"`
}

type Book struct {
	ID          int64   `json:"id"`
	AuthorID    int64   `json:"authorId"`
	Author      string  `json:"author"`
	SeriesID    *int64  `json:"seriesId,omitempty"`
	SeriesName  string  `json:"seriesName,omitempty"`
	SeriesNum   float64 `json:"seriesNum,omitempty"`
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	Language    string  `json:"language,omitempty"`
	Publisher   string  `json:"publisher,omitempty"`
	ReleaseDate string  `json:"releaseDate,omitempty"`
	GoodreadsID string  `json:"goodreadsId,omitempty"`
	HardcoverID string  `json:"hardcoverId,omitempty"`
	ISBN13      string  `json:"isbn13,omitempty"`
	// per-library presence, filled by list queries
	LibraryID  int64    `json:"libraryId,omitempty"`
	Monitored  bool     `json:"monitored"`
	FilePath   string   `json:"filePath,omitempty"`
	FileFormat string   `json:"fileFormat,omitempty"`
	FileSize   int64    `json:"fileSize,omitempty"`
	AddedAt    string   `json:"addedAt,omitempty"`
	Genres     []string `json:"genres,omitempty"`
	// provider popularity stat — refreshed, never manually edited
	RatingsCount int `json:"ratingsCount,omitempty"`
	// per-field refresh-protection locks; filled by GetBook only (list views
	// don't need it)
	FieldLocks map[string]bool `json:"fieldLocks,omitempty"`
}

type Library struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	RootPath         string `json:"rootPath"`
	QualityProfileID int64  `json:"qualityProfileId"`
	OPDSUsername     string `json:"opdsUsername"`
	// OPDSConfigured reports whether the feed's credentials are actually
	// usable (username present, password set) — the UI shows set vs change.
	OPDSConfigured bool `json:"opdsConfigured"`
	BookCount      int  `json:"bookCount"`
	OnShelf        int  `json:"onShelf"`
	ReviewCount    int  `json:"reviewCount"`
}

// ---- libraries & profiles ----

// EnsureDefaultProfile creates the "EPUB preferred" profile on first run.
func (s *Store) EnsureDefaultProfile() (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM quality_profiles ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil {
		return id, nil
	}
	res, err := s.db.Exec(`INSERT INTO quality_profiles (name, formats, cutoff_format, language, preferred_terms, avoided_terms)
		VALUES ('EPUB preferred', '["epub","azw3","mobi"]', 'epub', 'english', 'retail', 'scan, ocr')`)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) CreateLibrary(name, rootPath string, profileID int64, opdsUser, opdsHash string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO libraries (name, root_path, quality_profile_id, opds_username, opds_password_hash)
		VALUES (?, ?, ?, ?, ?)`, name, rootPath, profileID, opdsUser, opdsHash)
	if err != nil {
		return 0, fmt.Errorf("create library: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) ListLibraries() ([]Library, error) {
	rows, err := s.db.Query(`
		SELECT l.id, l.name, l.root_path, l.quality_profile_id, l.opds_username,
		       (l.opds_username != '' AND l.opds_password_hash != 'unset'),
		       COUNT(DISTINCT lb.id), COUNT(DISTINCT lb.file_path),
		       (SELECT COUNT(*) FROM import_files f WHERE f.library_id = l.id AND f.status = 'review')
		FROM libraries l
		LEFT JOIN library_books lb ON lb.library_id = l.id
		GROUP BY l.id ORDER BY l.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Library
	for rows.Next() {
		var l Library
		if err := rows.Scan(&l.ID, &l.Name, &l.RootPath, &l.QualityProfileID, &l.OPDSUsername, &l.OPDSConfigured, &l.BookCount, &l.OnShelf, &l.ReviewCount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---- upsert ----

// ErrAuthorGone means the author row a pinned upsert targeted no longer
// exists — the user deleted the author while its bibliography was still
// syncing. Callers stop rather than recreating the author by name (which
// resurrected deleted authors as "zombies" that still showed as added).
var ErrAuthorGone = errors.New("author no longer exists")

// UpsertBook finds-or-creates the author, series and book for meta, matching
// existing rows by goodreads id, hardcover id, isbn13, then author+title.
// Fields the user manually locked are never overwritten.
func (s *Store) UpsertBook(meta metadata.BookMeta) (int64, error) {
	if meta.Title == "" {
		return 0, fmt.Errorf("upsert: metadata has no title")
	}
	authorName := "Unknown Author"
	if len(meta.Authors) > 0 {
		authorName = meta.Authors[0]
	}
	authorID, err := s.upsertAuthor(authorName)
	if err != nil {
		return 0, err
	}
	return s.upsertBookWithAuthor(meta, authorID)
}

// UpsertBookForAuthor upserts a book pinned to an existing author id, never
// minting an author. If that author was deleted concurrently (a bibliography
// sync still running after the user removed the author), it returns
// ErrAuthorGone instead of recreating the row. The existence pre-check covers
// the common case; the foreign-key constraint on books.author_id is the
// race-proof backstop for a delete landing mid-upsert.
func (s *Store) UpsertBookForAuthor(meta metadata.BookMeta, authorID int64) (int64, error) {
	if meta.Title == "" {
		return 0, fmt.Errorf("upsert: metadata has no title")
	}
	var one int
	switch err := s.db.QueryRow(`SELECT 1 FROM authors WHERE id = ?`, authorID).Scan(&one); {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		return 0, ErrAuthorGone
	default:
		return 0, err
	}
	id, err := s.upsertBookWithAuthor(meta, authorID)
	if err != nil && isForeignKeyErr(err) {
		return 0, ErrAuthorGone
	}
	return id, err
}

// upsertBookWithAuthor is the shared body of the two upserts: everything after
// the author id is resolved. It never touches the authors table, so a pinned
// caller's deleted author is never recreated here.
func (s *Store) upsertBookWithAuthor(meta metadata.BookMeta, authorID int64) (int64, error) {
	var seriesID *int64
	if meta.SeriesName != "" && !metadata.Excluded(meta.SeriesName, s.excludeTerms()) {
		id, err := s.upsertSeries(authorID, meta.SeriesName)
		if err != nil {
			return 0, err
		}
		seriesID = &id
	}

	bookID, found, err := s.findBook(meta, authorID)
	if err != nil {
		return 0, err
	}
	if !found {
		res, err := s.db.Exec(`INSERT INTO books
			(author_id, series_id, series_num, title, description, language, publisher, release_date, goodreads_id, hardcover_id, isbn13, genres, ratings_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			authorID, seriesID, nullFloat(meta.SeriesIndex), meta.Title, meta.Description, meta.Language,
			meta.Publisher, nullStr(meta.ReleaseDate), nullStr(meta.GoodreadsID), nullStr(meta.HardcoverID), nullStr(meta.ISBN13), genresJSON(meta.Genres), meta.RatingsCount)
		if err != nil {
			return 0, fmt.Errorf("insert book: %w", err)
		}
		return res.LastInsertId()
	}

	// refresh path: update fields not locked by manual edits
	locks, err := s.fieldLocks(bookID)
	if err != nil {
		return 0, err
	}
	// A field is written only when it's not locked and the incoming value is
	// non-empty. Identities additionally only fill in — never overwrite an
	// existing one. seriesFlag also requires a resolved series.
	writeField := func(lock string, empty bool) bool { return !locks[lock] && !empty }
	seriesFlag := !locks["series"] && seriesID != nil
	var seriesIDVal any
	if seriesID != nil {
		seriesIDVal = *seriesID
	}

	// Static partial update: each column keeps its current value unless its
	// guard flag is true. No dynamic SQL; every value is a bound parameter.
	if _, err := s.db.Exec(`UPDATE books SET
		title        = CASE WHEN ? THEN ? ELSE title END,
		description  = CASE WHEN ? THEN ? ELSE description END,
		language     = CASE WHEN ? THEN ? ELSE language END,
		publisher    = CASE WHEN ? THEN ? ELSE publisher END,
		release_date = CASE WHEN ? THEN ? ELSE release_date END,
		genres       = CASE WHEN ? THEN ? ELSE genres END,
		ratings_count = CASE WHEN ? THEN ? ELSE ratings_count END,
		series_id    = CASE WHEN ? THEN ? ELSE series_id END,
		series_num   = CASE WHEN ? THEN ? ELSE series_num END,
		goodreads_id = CASE WHEN ? THEN ? ELSE goodreads_id END,
		hardcover_id = CASE WHEN ? THEN ? ELSE hardcover_id END,
		isbn13       = CASE WHEN ? THEN ? ELSE isbn13 END
		WHERE id = ?`,
		writeField("title", meta.Title == ""), meta.Title,
		writeField("description", meta.Description == ""), meta.Description,
		writeField("language", meta.Language == ""), meta.Language,
		writeField("publisher", meta.Publisher == ""), meta.Publisher,
		writeField("releaseDate", meta.ReleaseDate == ""), nullStr(meta.ReleaseDate),
		writeField("genres", len(meta.Genres) == 0), genresJSON(meta.Genres),
		meta.RatingsCount > 0, meta.RatingsCount,
		seriesFlag, seriesIDVal,
		seriesFlag, nullFloat(meta.SeriesIndex),
		meta.GoodreadsID != "" && !s.hasIdentity(bookID, "goodreads_id"), nullStr(meta.GoodreadsID),
		meta.HardcoverID != "" && !locks["hardcoverId"] && !s.hasIdentity(bookID, "hardcover_id"), nullStr(meta.HardcoverID),
		meta.ISBN13 != "" && !locks["isbn13"] && !s.hasIdentity(bookID, "isbn13"), nullStr(meta.ISBN13),
		bookID); err != nil {
		return 0, fmt.Errorf("update book: %w", err)
	}
	return bookID, nil
}

// MonitorState reports, without inserting anything, whether a search result
// already exists in the catalog and whether it's in a library / monitored —
// so the add dialog can show "In library" / "Monitored" instead of offering
// to add a duplicate. Identity matches the same keys as UpsertBook.
func (s *Store) MonitorState(meta metadata.BookMeta) (inLibrary, monitored bool) {
	var bookID int64
	found := false
	try := func(query, val string) bool {
		if val == "" {
			return false
		}
		if err := s.db.QueryRow(query, val).Scan(&bookID); err == nil {
			return true
		}
		return false
	}
	switch {
	case try(`SELECT id FROM books WHERE goodreads_id = ?`, meta.GoodreadsID):
		found = true
	case try(`SELECT id FROM books WHERE hardcover_id = ?`, meta.HardcoverID):
		found = true
	case try(`SELECT id FROM books WHERE isbn13 = ?`, meta.ISBN13):
		found = true
	default:
		if meta.Title != "" && len(meta.Authors) > 0 {
			err := s.db.QueryRow(`SELECT b.id FROM books b JOIN authors a ON a.id = b.author_id
				WHERE b.title = ? COLLATE NOCASE AND a.name = ? COLLATE NOCASE`,
				meta.Title, meta.Authors[0]).Scan(&bookID)
			found = err == nil
		}
	}
	if !found {
		return false, false
	}
	var count, mon int
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(monitored), 0) FROM library_books WHERE book_id = ?`, bookID).
		Scan(&count, &mon)
	return count > 0, mon == 1
}

// AuthorState reports whether an author already counts as added — for the
// "Add author" search results. The rule matches the Authors page exactly:
// monitored, or holding a book in some library. A bare leftover row (books
// removed one by one, an old mid-sync resurrection) is invisible on the
// Authors page, so search must offer "Add author" again rather than show an
// unremovable "Added" chip — re-adding finds the same row and re-monitors it.
func (s *Store) AuthorState(name string) (exists, monitored bool) {
	var id int64
	var mon int
	err := s.db.QueryRow(`SELECT id, monitored FROM authors WHERE name = ? COLLATE NOCASE`, name).Scan(&id, &mon)
	if err != nil {
		return false, false
	}
	if mon == 1 {
		return true, true
	}
	var inLibraries int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM books b
		JOIN library_books lb ON lb.book_id = b.id
		WHERE b.author_id = ?`, id).Scan(&inLibraries); err != nil || inLibraries == 0 {
		return false, false
	}
	return true, false
}

func (s *Store) findBook(meta metadata.BookMeta, authorID int64) (int64, bool, error) {
	var id int64
	tryQuery := func(query string, args ...any) (bool, error) {
		err := s.db.QueryRow(query, args...).Scan(&id)
		if err == sql.ErrNoRows {
			return false, nil
		}
		return err == nil, err
	}
	if meta.GoodreadsID != "" {
		if ok, err := tryQuery(`SELECT id FROM books WHERE goodreads_id = ?`, meta.GoodreadsID); ok || err != nil {
			return id, ok, err
		}
	}
	if meta.HardcoverID != "" {
		if ok, err := tryQuery(`SELECT id FROM books WHERE hardcover_id = ?`, meta.HardcoverID); ok || err != nil {
			return id, ok, err
		}
	}
	if meta.ISBN13 != "" {
		if ok, err := tryQuery(`SELECT id FROM books WHERE isbn13 = ?`, meta.ISBN13); ok || err != nil {
			return id, ok, err
		}
	}
	if ok, err := tryQuery(`SELECT id FROM books WHERE author_id = ? AND title = ? COLLATE NOCASE`, authorID, meta.Title); ok || err != nil {
		return id, ok, err
	}
	// Last resort: edition-variant identity. A re-sync can canonicalize a
	// different variant title than the row was created with ("In the Blood"
	// vs "In the Blood: A Thriller") while sharing none of the identifiers —
	// exact matching would mint a duplicate sibling row. The dedupe key is
	// series-aware and never merges distinct books.
	return s.findBookByDedupeKey(meta, authorID)
}

func (s *Store) findBookByDedupeKey(meta metadata.BookMeta, authorID int64) (int64, bool, error) {
	want := metadata.DedupeKey(meta.Title, meta.SeriesName)
	rows, err := s.db.Query(`SELECT b.id, b.title, COALESCE(se.name, '')
		FROM books b LEFT JOIN series se ON se.id = b.series_id
		WHERE b.author_id = ?`, authorID)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var title, seriesName string
		if err := rows.Scan(&id, &title, &seriesName); err != nil {
			return 0, false, err
		}
		if metadata.DedupeKey(title, seriesName) == want {
			return id, true, nil
		}
	}
	return 0, false, rows.Err()
}

// OverrideHardcoverID repoints a book's Hardcover identity. Unlike UpsertBook,
// which only fills a blank identity, this is the explicit re-match path — the
// user asked for the link to change, so an existing (possibly wrong) id is
// overwritten.
func (s *Store) OverrideHardcoverID(bookID int64, hardcoverID string) error {
	_, err := s.db.Exec(`UPDATE books SET hardcover_id = ? WHERE id = ?`, nullStr(hardcoverID), bookID)
	return err
}

// hasIdentity reports whether the book already carries the given identity, so
// UpsertBook can fill in blanks without ever overwriting one. Static query —
// the column is selected in Go, never concatenated into SQL.
func (s *Store) hasIdentity(bookID int64, col string) bool {
	var goodreads, hardcover, isbn sql.NullString
	if err := s.db.QueryRow(`SELECT goodreads_id, hardcover_id, isbn13 FROM books WHERE id = ?`, bookID).
		Scan(&goodreads, &hardcover, &isbn); err != nil {
		return false
	}
	switch col {
	case "goodreads_id":
		return goodreads.Valid && goodreads.String != ""
	case "hardcover_id":
		return hardcover.Valid && hardcover.String != ""
	case "isbn13":
		return isbn.Valid && isbn.String != ""
	default:
		return false
	}
}

func (s *Store) fieldLocks(bookID int64) (map[string]bool, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT field_locks FROM books WHERE id = ?`, bookID).Scan(&raw); err != nil {
		return nil, err
	}
	locks := map[string]bool{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &locks); err != nil {
			return nil, fmt.Errorf("field_locks for book %d: %w", bookID, err)
		}
	}
	return locks, nil
}

// EnsureAuthor finds-or-creates an author by name — the "add the author
// themselves" path from search, before any of their books exist.
func (s *Store) EnsureAuthor(name string) (int64, error) {
	return s.upsertAuthor(name)
}

func (s *Store) upsertAuthor(name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM authors WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO authors (name, sort_name) VALUES (?, ?)`, name, sortName(name))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) upsertSeries(authorID int64, name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM series WHERE author_id = ? AND name = ? COLLATE NOCASE`, authorID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO series (author_id, name) VALUES (?, ?)`, authorID, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// "Mara Voss" → "Voss, Mara"
func sortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	return last + ", " + strings.Join(parts[:len(parts)-1], " ")
}

// ---- library membership ----

func (s *Store) AddToLibrary(bookID, libraryID int64, monitored bool) error {
	if _, err := s.db.Exec(`INSERT INTO library_books (library_id, book_id, monitored) VALUES (?, ?, ?)
		ON CONFLICT(library_id, book_id) DO NOTHING`, libraryID, bookID, monitored); err != nil {
		return err
	}
	return s.markTracked(bookID)
}

// MarkAuthorAdded records that somebody asked for this author by name. It is
// what keeps them on the Authors page while their bibliography is still
// catalog-only — the one case where having nothing in a library is the point
// rather than a sign they should have dropped off.
func (s *Store) MarkAuthorAdded(authorID int64) error {
	_, err := s.db.Exec(`UPDATE authors SET added_manually = 1, monitored = 1 WHERE id = ?`, authorID)
	return err
}

// markTracked turns on the author's and series' tracking flags. Neither is a
// user decision any more: having a book in a library is what earns an author
// their weekly bibliography sync and a series its watch for new entries, and
// there is no reason anyone would want either off.
func (s *Store) markTracked(bookID int64) error {
	if _, err := s.db.Exec(`UPDATE authors SET monitored = 1
		WHERE id = (SELECT author_id FROM books WHERE id = ?)`, bookID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE series SET monitored = 1
		WHERE id = (SELECT series_id FROM books WHERE id = ?)`, bookID)
	return err
}

// SetMonitored flips a book's monitored flag. Monitoring a book that isn't
// in the library yet adds it — that's how catalog-only bibliography books
// enter a library from the author page. Unmonitoring never creates rows.
func (s *Store) SetMonitored(libraryID, bookID int64, monitored bool) error {
	if monitored {
		if _, err := s.db.Exec(`INSERT INTO library_books (library_id, book_id, monitored) VALUES (?, ?, 1)
			ON CONFLICT(library_id, book_id) DO UPDATE SET monitored = 1`, libraryID, bookID); err != nil {
			return err
		}
		return s.markTracked(bookID)
	}
	_, err := s.db.Exec(`UPDATE library_books SET monitored = 0 WHERE library_id = ? AND book_id = ?`,
		libraryID, bookID)
	return err
}

func (s *Store) SetFile(libraryID, bookID int64, path, format string, size int64) error {
	_, err := s.db.Exec(`UPDATE library_books SET file_path = ?, file_format = ?, file_size = ?
		WHERE library_id = ? AND book_id = ?`, path, format, size, libraryID, bookID)
	return err
}

// ---- queries ----

// ListAuthors returns the account's authors: monitored ones, and those with
// at least one book in a library. Stray catalog rows (co-author fallout,
// enrichment side effects) stay out of sight.
//
// Under a Visibility the rule narrows to that account: an author is theirs
// when a book of theirs sits in one of their libraries, or they added the
// author by hand. The counts narrow with it — a scoped user seeing "12 books,
// 8 on shelf" for an author whose books they can mostly not open would be
// both a leak and a lie.
func (s *Store) ListAuthors(v *Visibility) ([]Author, error) {
	ph, libArgs := idList(v.libraryIDs())
	all := v.unscoped()
	// A book counts as visible when it is in one of the account's libraries
	// or in none at all — a catalog-only bibliography entry, which is public
	// provider metadata rather than anyone's shelf.
	// G201 is about interpolating VALUES into SQL. The only thing spliced in
	// here is the "?,?,?" run idList builds from a slice length — the ids
	// themselves are bound below, like every other value in this file.
	query := fmt.Sprintf( //nolint:gosec // G201: placeholders only, see above
		`
		SELECT a.id, a.name, a.sort_name, a.monitored, a.bio, a.image_url,
		       COUNT(DISTINCT CASE WHEN ? = 1 OR lb.id IS NULL OR lb.library_id IN (%[1]s) THEN b.id END),
		       COUNT(DISTINCT CASE WHEN lb.file_path IS NOT NULL
		             AND (? = 1 OR lb.library_id IN (%[1]s)) THEN b.id END)
		FROM authors a
		LEFT JOIN books b ON b.author_id = a.id
		LEFT JOIN library_books lb ON lb.book_id = b.id
		GROUP BY a.id
		HAVING CASE WHEN ? = 1
		       THEN a.added_manually = 1 OR COUNT(DISTINCT lb.id) > 0
		       ELSE a.id IN (SELECT author_id FROM user_authors WHERE user_id = ?)
		            OR COUNT(DISTINCT CASE WHEN lb.library_id IN (%[1]s) THEN lb.id END) > 0
		       END
		ORDER BY a.sort_name`, ph)
	args := []any{all}
	args = append(args, libArgs...)
	args = append(args, all)
	args = append(args, libArgs...)
	args = append(args, all, v.userID())
	args = append(args, libArgs...)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Author
	for rows.Next() {
		var a Author
		var imageURL string
		if err := rows.Scan(&a.ID, &a.Name, &a.SortName, &a.Monitored, &a.Bio, &imageURL, &a.BookCount, &a.OnShelf); err != nil {
			return nil, err
		}
		a.HasPhoto = imageURL != ""
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAuthorInfo stores the presentation data synced from Hardcover and stamps
// the sync time. Empty values are written as-is — a provider that stopped
// carrying a bio shouldn't leave a stale one behind.
func (s *Store) SetAuthorInfo(authorID int64, bio, imageURL string) error {
	_, err := s.db.Exec(`UPDATE authors SET bio = ?, image_url = ?, info_synced_at = datetime('now') WHERE id = ?`,
		bio, imageURL, authorID)
	return err
}

// AuthorImageURL returns the provider source URL for the author's portrait,
// or "" when none is known — the photo endpoint fetches-and-caches from it.
func (s *Store) AuthorImageURL(authorID int64) string {
	var url string
	if err := s.db.QueryRow(`SELECT image_url FROM authors WHERE id = ?`, authorID).Scan(&url); err != nil {
		return ""
	}
	return url
}

// AuthorInfoDue reports whether the author's presentation data has never been
// synced or is older than 30 days.
func (s *Store) AuthorInfoDue(authorID int64) bool {
	var due int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM authors WHERE id = ?
		AND (info_synced_at IS NULL OR info_synced_at < datetime('now', '-30 days'))`, authorID).Scan(&due)
	return err == nil && due > 0
}

// ListSeries returns the account's series: monitored ones, and those with at
// least one book in a library. Series that exist only through catalog-only
// bibliography books stay on the author page until a book is shelved.
//
// Under a Visibility a series belongs to the account when one of its books is
// in one of their libraries, or its author is one they added. Counts and the
// index page's cover fan narrow the same way, so a series never advertises
// books from a library the account can't open.
func (s *Store) ListSeries(v *Visibility) ([]Series, error) {
	ph, libArgs := idList(v.libraryIDs())
	all := v.unscoped()
	// G201 is about interpolating VALUES into SQL. The only thing spliced in
	// here is the "?,?,?" run idList builds from a slice length — the ids
	// themselves are bound below, like every other value in this file.
	query := fmt.Sprintf( //nolint:gosec // G201: placeholders only, see above
		`
		SELECT sr.id, sr.author_id, a.name, sr.name, sr.monitored,
		       COUNT(DISTINCT CASE WHEN ? = 1 OR lb.id IS NULL OR lb.library_id IN (%[1]s) THEN b.id END),
		       COUNT(DISTINCT CASE WHEN lb.file_path IS NOT NULL
		             AND (? = 1 OR lb.library_id IN (%[1]s)) THEN b.id END),
		       COALESCE((SELECT group_concat(id) FROM (
		           SELECT b2.id AS id FROM books b2
		           WHERE b2.series_id = sr.id
		             AND (? = 1
		                  OR NOT EXISTS (SELECT 1 FROM library_books x WHERE x.book_id = b2.id)
		                  OR EXISTS (SELECT 1 FROM library_books x
		                             WHERE x.book_id = b2.id AND x.library_id IN (%[1]s)))
		           ORDER BY COALESCE(b2.series_num, 1e9), b2.id LIMIT 3)), '')
		FROM series sr
		JOIN authors a ON a.id = sr.author_id
		LEFT JOIN books b ON b.series_id = sr.id
		LEFT JOIN library_books lb ON lb.book_id = b.id
		GROUP BY sr.id
		HAVING CASE WHEN ? = 1
		       THEN COUNT(DISTINCT lb.id) > 0
		       ELSE COUNT(DISTINCT CASE WHEN lb.library_id IN (%[1]s) THEN lb.id END) > 0
		       END
		ORDER BY sr.name`, ph)
	args := []any{all}
	args = append(args, libArgs...)
	args = append(args, all)
	args = append(args, libArgs...)
	args = append(args, all)
	args = append(args, libArgs...)
	args = append(args, all)
	args = append(args, libArgs...)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Series
	for rows.Next() {
		var sr Series
		var coverIDs string
		if err := rows.Scan(&sr.ID, &sr.AuthorID, &sr.Author, &sr.Name, &sr.Monitored, &sr.Total, &sr.OnShelf, &coverIDs); err != nil {
			return nil, err
		}
		// the first few in-order books, so the index can show a cover fan
		for _, part := range strings.Split(coverIDs, ",") {
			if id, err := strconv.ParseInt(part, 10, 64); err == nil {
				sr.CoverBookIDs = append(sr.CoverBookIDs, id)
			}
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

const bookColumns = `
	b.id, b.author_id, a.name, b.series_id, COALESCE(sr.name, ''), COALESCE(b.series_num, 0),
	b.title, b.description, b.language, b.publisher, COALESCE(b.release_date, ''),
	COALESCE(b.goodreads_id, ''), COALESCE(b.hardcover_id, ''), COALESCE(b.isbn13, ''), b.genres, b.ratings_count`

func scanBook(scan func(...any) error, b *Book) error {
	var genres string
	if err := scan(&b.ID, &b.AuthorID, &b.Author, &b.SeriesID, &b.SeriesName, &b.SeriesNum,
		&b.Title, &b.Description, &b.Language, &b.Publisher, &b.ReleaseDate,
		&b.GoodreadsID, &b.HardcoverID, &b.ISBN13, &genres, &b.RatingsCount); err != nil {
		return err
	}
	if genres != "" {
		_ = json.Unmarshal([]byte(genres), &b.Genres) //nolint:errcheck // bad rows read as no genres
	}
	return nil
}

func genresJSON(genres []string) string {
	if len(genres) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(genres)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func (s *Store) GetBook(id int64) (*Book, error) {
	row := s.db.QueryRow(`SELECT `+bookColumns+`
		FROM books b JOIN authors a ON a.id = b.author_id
		LEFT JOIN series sr ON sr.id = b.series_id WHERE b.id = ?`, id)
	var b Book
	if err := scanBook(row.Scan, &b); err != nil {
		return nil, err
	}
	// Fill primary library presence: refresh/edit endpoints echo the book back
	// to the UI, which must not lose file/monitor state (the detail page showed
	// the file vanishing after "refresh metadata" until a page reload). Prefer
	// the membership holding a file, so "on shelf" survives; no membership
	// leaves the zero values, as before.
	_ = s.db.QueryRow(`SELECT library_id, monitored, COALESCE(file_path, ''), COALESCE(file_format, ''), COALESCE(file_size, 0), COALESCE(added_at, '')
		FROM library_books WHERE book_id = ?
		ORDER BY (COALESCE(file_path, '') = ''), library_id LIMIT 1`, id).
		Scan(&b.LibraryID, &b.Monitored, &b.FilePath, &b.FileFormat, &b.FileSize, &b.AddedAt)
	if locks, err := s.fieldLocks(id); err == nil && len(locks) > 0 {
		b.FieldLocks = locks
	}
	return &b, nil
}

// ListBooks returns books, optionally filtered by author, library or series,
// with per-library file/monitor state when the book is in a library.
func (s *Store) ListBooks(authorID, libraryID, seriesID int64) ([]Book, error) {
	// Static query — each optional filter is disabled by passing 0, so there's
	// no dynamic SQL to build and every value stays a bound parameter.
	const query = `SELECT ` + bookColumns + `,
		COALESCE(lb.library_id, 0), COALESCE(lb.monitored, 0),
		COALESCE(lb.file_path, ''), COALESCE(lb.file_format, ''), COALESCE(lb.file_size, 0),
		COALESCE(lb.added_at, '')
		FROM books b
		JOIN authors a ON a.id = b.author_id
		LEFT JOIN series sr ON sr.id = b.series_id
		LEFT JOIN library_books lb ON lb.book_id = b.id
		WHERE (? = 0 OR b.author_id = ?)
		  AND (? = 0 OR lb.library_id = ?)
		  AND (? = 0 OR b.series_id = ?)
		ORDER BY COALESCE(b.release_date, '9999') DESC, b.title`

	rows, err := s.db.Query(query, authorID, authorID, libraryID, libraryID, seriesID, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Book
	for rows.Next() {
		var b Book
		var genres string
		err := rows.Scan(&b.ID, &b.AuthorID, &b.Author, &b.SeriesID, &b.SeriesName, &b.SeriesNum,
			&b.Title, &b.Description, &b.Language, &b.Publisher, &b.ReleaseDate,
			&b.GoodreadsID, &b.HardcoverID, &b.ISBN13, &genres, &b.RatingsCount,
			&b.LibraryID, &b.Monitored, &b.FilePath, &b.FileFormat, &b.FileSize, &b.AddedAt)
		if err != nil {
			return nil, err
		}
		if genres != "" {
			_ = json.Unmarshal([]byte(genres), &b.Genres) //nolint:errcheck
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) AddHistory(bookID, libraryID int64, kind, detail string) error {
	_, err := s.db.Exec(`INSERT INTO history (book_id, library_id, kind, detail) VALUES (?, ?, ?, ?)`,
		nullID(bookID), nullID(libraryID), kind, detail)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
