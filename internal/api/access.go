package api

import (
	"errors"
	"net/http"

	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/catalog"
)

// Authorization sits on two independent axes.
//
// Role decides who configures the install: admins own settings, libraries,
// quality profiles, watched lists, backups, accounts, and anything that edits
// metadata by hand or deletes something. A plain user never sees any of it.
//
// Library scope decides where a plain user may work: the libraries assigned
// to their account when it was created. Inside those they do the everyday
// things — browse, search and add books, refresh metadata from the providers,
// monitor authors and series, grab releases, pair a KoReader device.
//
// Before the first account exists the whole API is open so the setup wizard
// can run; every check here answers yes in that state.

type access struct {
	// open is the pre-auth state: no accounts exist yet.
	open bool
	user *auth.User
}

func (s *Server) access(r *http.Request) access {
	if !s.Auth.Enabled() {
		return access{open: true}
	}
	return access{user: s.sessionUser(r)}
}

func (a access) admin() bool { return a.open || a.user.IsAdmin() }

func (a access) mayLibrary(libraryID int64) bool { return a.open || a.user.MayAccess(libraryID) }

// scoped reports whether list results have to be filtered before going out.
// Admins and the pre-auth wizard see everything, so most handlers can skip
// the work entirely.
func (a access) scoped() bool { return !a.admin() }

var errForbidden = errors.New("you don't have access to this library")

// requireAdmin guards install-wide configuration and anything destructive.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.access(r).admin() {
		return true
	}
	writeErr(w, http.StatusForbidden, errors.New("admin access required"))
	return false
}

// requireLibrary guards work inside one library.
func (s *Server) requireLibrary(w http.ResponseWriter, r *http.Request, libraryID int64) bool {
	if s.access(r).mayLibrary(libraryID) {
		return true
	}
	writeErr(w, http.StatusForbidden, errForbidden)
	return false
}

// requireBook guards a per-book action, resolving the book's libraries first.
func (s *Server) requireBook(w http.ResponseWriter, r *http.Request, bookID int64) bool {
	if s.mayBook(s.access(r), bookID) {
		return true
	}
	writeErr(w, http.StatusForbidden, errors.New("you don't have access to this book"))
	return false
}

// mayBook reports whether the caller may see a book. A book with no library
// membership is catalog-only — a bibliography entry nobody has shelved — and
// follows its author, so the author pages a user may browse stay whole while
// an author they can't see doesn't leak titles.
func (s *Server) mayBook(a access, bookID int64) bool {
	if a.admin() {
		return true
	}
	libs, err := s.bookLibraries(bookID)
	if err != nil {
		return false
	}
	if len(libs) == 0 {
		var authorID int64
		if err := s.db.QueryRow(`SELECT author_id FROM books WHERE id = ?`, bookID).Scan(&authorID); err != nil {
			return false
		}
		return s.mayAuthor(a, authorID)
	}
	for _, id := range libs {
		if a.mayLibrary(id) {
			return true
		}
	}
	return false
}

// visibility renders the caller's access into the filter the catalog layer
// takes. nil means no narrowing — admins and the pre-auth wizard.
func (s *Server) visibility(a access) *catalog.Visibility {
	if !a.scoped() {
		return nil
	}
	v := &catalog.Visibility{}
	if a.user != nil {
		v.LibraryIDs = a.user.Libraries
		v.UserID = a.user.ID
	}
	return v
}

// mayAuthor mirrors the rule ListAuthors applies: an author belongs to an
// account when one of their books sits in one of that account's libraries, or
// the account added the author by hand. Acting on an author you can't see —
// monitoring them, re-syncing their bibliography, searching their backlist —
// is refused the same way reading them is.
func (s *Server) mayAuthor(a access, authorID int64) bool {
	if a.admin() {
		return true
	}
	if a.user == nil {
		return false
	}
	var added int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_authors WHERE user_id = ? AND author_id = ?`,
		a.user.ID, authorID).Scan(&added); err == nil && added > 0 {
		return true
	}
	return s.anyLibraryAllowed(a, `SELECT DISTINCT lb.library_id FROM library_books lb
		JOIN books b ON b.id = lb.book_id WHERE b.author_id = ?`, authorID)
}

// maySeries is the same rule one level down: a series the account can reach
// through its books, or one belonging to an author they added.
func (s *Server) maySeries(a access, seriesID int64) bool {
	if a.admin() {
		return true
	}
	if a.user == nil {
		return false
	}
	var added int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_authors ua
		JOIN series sr ON sr.author_id = ua.author_id
		WHERE ua.user_id = ? AND sr.id = ?`, a.user.ID, seriesID).Scan(&added); err == nil && added > 0 {
		return true
	}
	return s.anyLibraryAllowed(a, `SELECT DISTINCT lb.library_id FROM library_books lb
		JOIN books b ON b.id = lb.book_id WHERE b.series_id = ?`, seriesID)
}

// anyLibraryAllowed reports whether any library id the query returns is one
// the caller holds. The membership check stays in Go so the query itself has
// no id list spliced into it.
func (s *Server) anyLibraryAllowed(a access, query string, arg int64) bool {
	rows, err := s.db.Query(query, arg)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var libID int64
		if err := rows.Scan(&libID); err != nil {
			return false
		}
		if a.mayLibrary(libID) {
			return true
		}
	}
	return false
}

func (s *Server) requireAuthor(w http.ResponseWriter, r *http.Request, authorID int64) bool {
	if s.mayAuthor(s.access(r), authorID) {
		return true
	}
	writeErr(w, http.StatusForbidden, errors.New("you don't have access to this author"))
	return false
}

func (s *Server) requireSeries(w http.ResponseWriter, r *http.Request, seriesID int64) bool {
	if s.maySeries(s.access(r), seriesID) {
		return true
	}
	writeErr(w, http.StatusForbidden, errors.New("you don't have access to this series"))
	return false
}

func (s *Server) bookLibraries(bookID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT library_id FROM library_books WHERE book_id = ?`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// bookScope filters book lists for one request. It preloads the authors the
// account can see, because judging a catalog-only row needs that answer and
// asking per row would be a query per book.
type bookScope struct {
	access
	authors map[int64]bool
}

func (s *Server) bookScope(a access) bookScope {
	sc := bookScope{access: a}
	if !a.scoped() {
		return sc
	}
	sc.authors = map[int64]bool{}
	if a.user == nil {
		return sc
	}
	// authors reached through the account's libraries...
	rows, err := s.db.Query(`SELECT DISTINCT b.author_id, lb.library_id
		FROM books b JOIN library_books lb ON lb.book_id = b.id`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var authorID, libID int64
			if err := rows.Scan(&authorID, &libID); err != nil {
				break
			}
			if a.mayLibrary(libID) {
				sc.authors[authorID] = true
			}
		}
	}
	// ...plus the ones they added by hand, who may have nothing shelved yet
	added, err := s.db.Query(`SELECT author_id FROM user_authors WHERE user_id = ?`, a.user.ID)
	if err == nil {
		defer added.Close()
		for added.Next() {
			var authorID int64
			if err := added.Scan(&authorID); err != nil {
				break
			}
			sc.authors[authorID] = true
		}
	}
	return sc
}

// filter drops rows the caller may not see. ListBooks emits one row per
// library membership, so a book shelved in both an allowed and a denied
// library keeps only the allowed row — which is what the UI should show.
//
// LibraryID 0 is a catalog-only row: a bibliography entry sitting in no
// library at all. Those follow their author, matching the Authors page — an
// author you can see brings their whole bibliography with them, and one you
// can't doesn't advertise their titles through the book list.
func (sc bookScope) filter(books []catalog.Book) []catalog.Book {
	if !sc.scoped() {
		return books
	}
	out := books[:0:0]
	for _, b := range books {
		if b.LibraryID == 0 {
			if sc.authors[b.AuthorID] {
				out = append(out, b)
			}
			continue
		}
		if sc.mayLibrary(b.LibraryID) {
			out = append(out, b)
		}
	}
	return out
}

// filterLibraries narrows the library list to what the caller may reach.
func (a access) filterLibraries(libs []catalog.Library) []catalog.Library {
	if !a.scoped() {
		return libs
	}
	out := libs[:0:0]
	for _, l := range libs {
		if a.mayLibrary(l.ID) {
			out = append(out, l)
		}
	}
	return out
}
