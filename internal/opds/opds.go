// Package opds serves an OPDS 1.2 catalog per account. A reader app signs in
// with the account's username and its OPDS password — a separate password
// from the web login, since reader apps store it in plain text — and sees
// exactly the books the account's tags cover. Any OPDS reader (KOReader,
// Moon+, Thorium…) can browse and download that shelf.
package opds

import (
	"database/sql"
	"encoding/xml"
	"fmt"
	"log"
	"net"
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
	auth    *auth.Store
}

func New(db *sql.DB, cat *catalog.Store, covers *catalog.CoverCache, accounts *auth.Store) *Handler {
	return &Handler{db: db, catalog: cat, covers: covers, auth: accounts}
}

// Register mounts the OPDS routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /opds", h.handleFeed)
	mux.HandleFunc("GET /opds/download/{bookId}", h.handleDownload)
	mux.HandleFunc("GET /opds/cover/{bookId}", h.handleCover)
	// the per-library feeds that came before tags: answer reader apps still
	// pointed at them with a reason, not the web app's HTML
	mux.HandleFunc("GET /opds/{libraryId}", h.handleMoved)
	mux.HandleFunc("GET /opds/{libraryId}/download/{bookId}", h.handleMoved)
	mux.HandleFunc("GET /opds/{libraryId}/cover/{bookId}", h.handleMoved)
}

func (h *Handler) handleMoved(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "This feed moved to /opds — sign in with your own username and OPDS password (Settings → Account in Booky).", http.StatusGone)
}

// authorize checks the request's Basic credentials against an account's
// OPDS password (with lockout after repeated failures) and returns the
// account with its CURRENT tags. An account that never set an OPDS password
// can't sign in — a feed must be opted into.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	user, pass, hasAuth := r.BasicAuth()
	if hasAuth {
		ip := r.RemoteAddr
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
		u, err := h.auth.OPDSLogin(user, pass, ip)
		if err == nil {
			return u, true
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="booky"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
	return nil, false
}

// mayRead is the per-book rule: admins read everything on the shelf, anyone
// else needs one of the book's tags.
func (h *Handler) mayRead(u *auth.User, b *catalog.Book) bool {
	if u.IsAdmin() {
		return true
	}
	for _, t := range b.Tags {
		if u.HasTag(t) {
			return true
		}
	}
	return false
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
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	books, err := h.catalog.ListBooks(0, 0, 0)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}

	self := "/opds"
	now := time.Now().UTC().Format(time.RFC3339)
	f := feed{
		Xmlns:   "http://www.w3.org/2005/Atom",
		XmlnsDC: "http://purl.org/dc/terms/",
		ID:      "urn:booky:user:" + strconv.FormatInt(u.ID, 10),
		Title:   u.Username + "'s books — Booky",
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
		if !h.mayRead(u, &b) {
			continue
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

// fileFor returns a book's file if the account may read it. Every download
// and cover request comes through here, so a guessed URL for someone else's
// book gets the same 404 as one that doesn't exist.
func (h *Handler) fileFor(u *auth.User, bookID int64) (path, format string, err error) {
	b, err := h.catalog.GetBook(bookID)
	if err != nil || b.FilePath == "" || !h.mayRead(u, b) {
		return "", "", fmt.Errorf("no file")
	}
	return b.FilePath, b.FileFormat, nil
}

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	bookID, err := strconv.ParseInt(r.PathValue("bookId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, format, err := h.fileFor(u, bookID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mimeFor(format))
	w.Header().Set("Content-Disposition", `attachment; filename="`+SanitizeFilename(filepath.Base(path))+`"`)
	http.ServeFile(w, r, path)
}

func (h *Handler) handleCover(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	bookID, err := strconv.ParseInt(r.PathValue("bookId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// only books on the account's shelf expose covers on its feed
	if _, _, err := h.fileFor(u, bookID); err != nil {
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

// SanitizeFilename keeps header injection out of Content-Disposition.
func SanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 {
			return '_'
		}
		return r
	}, name)
}
