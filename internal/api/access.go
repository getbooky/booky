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
// Tags decide what a plain user may see. There is one library; a shelved book
// is visible to an account when it carries one of the account's tags (their
// personal tag included), and a book with no tags is admin-only. Within what
// they can see, users do the everyday things — browse, search and add books,
// refresh metadata from the providers, grab releases, pair a device.
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

// tags is what the caller holds; empty for the pre-auth wizard, which is
// admin anyway.
func (a access) tags() []int64 {
	if a.user == nil {
		return nil
	}
	return a.user.Tags
}

// mayLibrary only asks whether the caller may work in the library at all —
// any signed-in account may, since there is just the one. What they may touch
// inside it is decided per book.
func (a access) mayLibrary(libraryID int64) bool { return a.open || a.user != nil }

// scoped reports whether list results have to be filtered before going out.
// Admins and the pre-auth wizard see everything, so most handlers can skip
// the work entirely.
func (a access) scoped() bool { return !a.admin() }

var errForbidden = errors.New("you don't have access to this")

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

// requireBook guards a per-book action.
func (s *Server) requireBook(w http.ResponseWriter, r *http.Request, bookID int64) bool {
	if s.mayBook(s.access(r), bookID) {
		return true
	}
	writeErr(w, http.StatusForbidden, errors.New("you don't have access to this book"))
	return false
}

// mayBook reports whether the caller may see a book. A shelved book needs one
// of the caller's tags. A book that isn't shelved is catalog-only — a
// bibliography entry nobody has added — and follows its author, so the author
// pages a user may browse stay whole while an author they can't see doesn't
// leak titles.
func (s *Server) mayBook(a access, bookID int64) bool {
	if a.admin() {
		return true
	}
	if a.user == nil {
		return false
	}
	var shelved int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM library_books WHERE book_id = ?`, bookID).Scan(&shelved); err != nil {
		return false
	}
	if shelved == 0 {
		var authorID int64
		if err := s.db.QueryRow(`SELECT author_id FROM books WHERE id = ?`, bookID).Scan(&authorID); err != nil {
			return false
		}
		return s.mayAuthor(a, authorID)
	}
	return s.Catalog.BookVisible(bookID, a.user.Tags)
}

// mayFile reports whether the caller may download a book's file. Stricter
// than mayBook: a catalog-only book has no file to give, and a shelved one
// must carry the caller's tag — following the author is for browsing only.
func (s *Server) mayFile(a access, bookID int64) bool {
	if a.admin() {
		return true
	}
	return a.user != nil && s.Catalog.BookVisible(bookID, a.user.Tags)
}

// visibility renders the caller's access into the filter the catalog layer
// takes. nil means no narrowing — admins and the pre-auth wizard.
func (s *Server) visibility(a access) *catalog.Visibility {
	if !a.scoped() {
		return nil
	}
	v := &catalog.Visibility{}
	if a.user != nil {
		v.TagIDs = a.user.Tags
		v.UserID = a.user.ID
	}
	return v
}

// mayAuthor mirrors the rule ListAuthors applies: an author belongs to an
// account when one of their books carries one of the account's tags, or the
// account added the author by hand. Acting on an author you can't see —
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
	return s.anyTagAllowed(a, `SELECT DISTINCT bt.tag_id FROM book_tags bt
		JOIN books b ON b.id = bt.book_id WHERE b.author_id = ?`, authorID)
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
	return s.anyTagAllowed(a, `SELECT DISTINCT bt.tag_id FROM book_tags bt
		JOIN books b ON b.id = bt.book_id WHERE b.series_id = ?`, seriesID)
}

// anyTagAllowed reports whether any tag id the query returns is one the
// caller holds. The membership check stays in Go so the query itself has no
// id list spliced into it.
func (s *Server) anyTagAllowed(a access, query string, arg int64) bool {
	rows, err := s.db.Query(query, arg)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var tagID int64
		if err := rows.Scan(&tagID); err != nil {
			return false
		}
		if a.user.HasTag(tagID) {
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
	// authors reached through the account's tags...
	rows, err := s.db.Query(`SELECT DISTINCT b.author_id, bt.tag_id
		FROM books b JOIN book_tags bt ON bt.book_id = b.id`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var authorID, tagID int64
			if err := rows.Scan(&authorID, &tagID); err != nil {
				break
			}
			if a.user.HasTag(tagID) {
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

// filter drops rows the caller may not see: a shelved book needs one of the
// caller's tags.
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
		for _, t := range b.Tags {
			if sc.user.HasTag(t) {
				out = append(out, b)
				break
			}
		}
	}
	return out
}

// redactTags hides tags a plain user doesn't hold: which other audiences a
// book reaches — another person's personal tag above all — is the admin's
// business, not theirs.
func (a access) redactTags(books []catalog.Book) {
	if a.admin() {
		return
	}
	for i := range books {
		kept := []int64{}
		for _, t := range books[i].Tags {
			if a.user.HasTag(t) {
				kept = append(kept, t)
			}
		}
		books[i].Tags = kept
	}
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

// shelveFor makes sure a book a plain user just pulled into the library —
// added, monitored, grabbed — is one they can see: if none of their tags is on
// it yet, it gets their personal tag. Without this a user's own grab would
// land as an admin-only book. Admins see everything and tag deliberately, so
// nothing is added for them.
func (s *Server) shelveFor(a access, bookID int64) {
	if a.admin() || a.user == nil || a.user.PersonalTag == 0 {
		return
	}
	if s.Catalog.BookVisible(bookID, a.user.Tags) {
		return
	}
	if ch, err := s.Catalog.AddBookTags(bookID, []int64{a.user.PersonalTag}); err == nil {
		s.tagsChanged([]catalog.TagChange{ch}, true)
	}
}
