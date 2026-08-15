// Package opds serves one OPDS 1.2 catalog per library, guarded by that
// library's own HTTP basic credentials — never a user account. Any OPDS
// reader (KOReader, Moon+, Thorium…) can browse and download the shelf.
package opds

import (
	"database/sql"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/catalog"
)

type Handler struct {
	db      *sql.DB
	catalog *catalog.Store
	covers  *catalog.CoverCache
}

func New(db *sql.DB, cat *catalog.Store, covers *catalog.CoverCache) *Handler {
	return &Handler{db: db, catalog: cat, covers: covers}
}

// Register mounts the OPDS routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /opds/{libraryId}", h.handleFeed)
	mux.HandleFunc("GET /opds/{libraryId}/download/{bookId}", h.handleDownload)
	mux.HandleFunc("GET /opds/{libraryId}/cover/{bookId}", h.handleCover)
}

// authorize checks the request's basic auth against the library's OPDS
// credentials. Libraries whose password was never set (hash "unset") refuse
// everything — a feed must be opted into.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) (libraryID int64, ok bool) {
	libraryID, err := strconv.ParseInt(r.PathValue("libraryId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	var wantUser, hash string
	err = h.db.QueryRow(`SELECT opds_username, opds_password_hash FROM libraries WHERE id = ?`, libraryID).
		Scan(&wantUser, &hash)
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	user, pass, hasAuth := r.BasicAuth()
	if hash == "unset" || !hasAuth || user != wantUser || !auth.CheckPassword(hash, pass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="booky"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return 0, false
	}
	return libraryID, true
}

// ---- feed rendering ----

type feed struct {
	XMLName xml.Name `xml:"feed"`
	Xmlns   string   `xml:"xmlns,attr"`
	XmlnsDC string   `xml:"xmlns:dc,attr"`
	ID      string   `xml:"id"`
	Title   string   `xml:"title"`
	Updated string   `xml:"updated"`
	Links   []link   `xml:"link"`
	Entries []entry  `xml:"entry"`
}

type link struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr"`
}

type entry struct {
	ID       string   `xml:"id"`
	Title    string   `xml:"title"`
	Updated  string   `xml:"updated"`
	Authors  []person `xml:"author"`
	Series   string   `xml:"dc:extent,omitempty"` // human-readable series label
	Language string   `xml:"dc:language,omitempty"`
	Summary  *text    `xml:"summary,omitempty"`
	Links    []link   `xml:"link"`
}

type person struct {
	Name string `xml:"name"`
}

type text struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

var formatMIME = map[string]string{
	"epub": "application/epub+zip",
	"azw3": "application/x-mobi8-ebook",
	"azw":  "application/x-mobi8-ebook",
	"mobi": "application/x-mobipocket-ebook",
	"pdf":  "application/pdf",
	"cbz":  "application/vnd.comicbook+zip",
	"fb2":  "text/fb2+xml",
	"txt":  "text/plain",
}

func mimeFor(format string) string {
	if m, ok := formatMIME[strings.ToLower(format)]; ok {
		return m
	}
	return "application/octet-stream"
}

func (h *Handler) handleFeed(w http.ResponseWriter, r *http.Request) {
	libraryID, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var libName string
	if err := h.db.QueryRow(`SELECT name FROM libraries WHERE id = ?`, libraryID).Scan(&libName); err != nil {
		http.NotFound(w, r)
		return
	}
	books, err := h.catalog.ListBooks(0, libraryID, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	self := fmt.Sprintf("/opds/%d", libraryID)
	now := time.Now().UTC().Format(time.RFC3339)
	f := feed{
		Xmlns:   "http://www.w3.org/2005/Atom",
		XmlnsDC: "http://purl.org/dc/terms/",
		ID:      "urn:booky:library:" + strconv.FormatInt(libraryID, 10),
		Title:   libName + " — Booky",
		Updated: now,
		Links: []link{
			{Rel: "self", Href: self, Type: "application/atom+xml;profile=opds-catalog;kind=acquisition"},
			{Rel: "start", Href: self, Type: "application/atom+xml;profile=opds-catalog;kind=acquisition"},
		},
	}
	for _, b := range books {
		if b.FilePath == "" {
			continue // shelf only — wanted books aren't downloadable
		}
		e := entry{
			ID:       fmt.Sprintf("urn:booky:book:%d", b.ID),
			Title:    b.Title,
			Updated:  now,
			Authors:  []person{{Name: b.Author}},
			Language: b.Language,
			Links: []link{
				{
					Rel:  "http://opds-spec.org/acquisition",
					Href: fmt.Sprintf("%s/download/%d", self, b.ID),
					Type: mimeFor(b.FileFormat),
				},
			},
		}
		if b.SeriesName != "" {
			label := b.SeriesName
			if b.SeriesNum != 0 {
				label = fmt.Sprintf("%s #%g", b.SeriesName, b.SeriesNum)
			}
			e.Series = label
		}
		if b.Description != "" {
			e.Summary = &text{Type: "text", Body: b.Description}
		}
		if h.covers.Path(b.ID) != "" {
			cover := fmt.Sprintf("%s/cover/%d", self, b.ID)
			e.Links = append(e.Links,
				link{Rel: "http://opds-spec.org/image", Href: cover, Type: "image/jpeg"},
				link{Rel: "http://opds-spec.org/image/thumbnail", Href: cover, Type: "image/jpeg"},
			)
		}
		f.Entries = append(f.Entries, e)
	}

	w.Header().Set("Content-Type", "application/atom+xml;profile=opds-catalog;kind=acquisition; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		return
	}
	if err := xml.NewEncoder(w).Encode(f); err != nil {
		log.Printf("opds: encode feed: %v", err)
	}
}

// fileFor returns the library's file for a book, guarding against rows from
// other libraries.
func (h *Handler) fileFor(libraryID, bookID int64) (path, format string, err error) {
	var p, f sql.NullString
	err = h.db.QueryRow(`SELECT file_path, file_format FROM library_books
		WHERE library_id = ? AND book_id = ?`, libraryID, bookID).Scan(&p, &f)
	if err != nil || !p.Valid || p.String == "" {
		return "", "", fmt.Errorf("no file")
	}
	return p.String, f.String, nil
}

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	libraryID, ok := h.authorize(w, r)
	if !ok {
		return
	}
	bookID, err := strconv.ParseInt(r.PathValue("bookId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, format, err := h.fileFor(libraryID, bookID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mimeFor(format))
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(filepath.Base(path))+`"`)
	http.ServeFile(w, r, path)
}

func (h *Handler) handleCover(w http.ResponseWriter, r *http.Request) {
	libraryID, ok := h.authorize(w, r)
	if !ok {
		return
	}
	bookID, err := strconv.ParseInt(r.PathValue("bookId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// only books actually in this library expose covers on its feed
	if _, _, err := h.fileFor(libraryID, bookID); err != nil {
		http.NotFound(w, r)
		return
	}
	path := h.covers.Path(bookID)
	if path == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

// sanitizeFilename keeps header injection out of Content-Disposition.
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 {
			return '_'
		}
		return r
	}, name)
}
