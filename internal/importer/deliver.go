// Deliver moves a downloaded file into its library: named by the user's
// scheme, metadata written into the file, and hard-linked into every other
// library that wants the same book — one download, one set of bytes.
package importer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/epub"
)

// NamingSettings is the slice of app settings Deliver needs; the api layer
// reads them fresh per call so edits apply immediately.
type NamingSettings struct {
	Scheme        string      // e.g. "{Author}/{Title}", extension always appended
	WriteOnImport bool        // write metadata into the file during delivery
	WriteFields   epub.Fields // which metadata groups to write
	CoverFile     string      // cached cover image to embed when the cover field is on
}

// unsafePathChars strips characters that break paths on common filesystems.
var unsafeReplacer = strings.NewReplacer("/", "-", "\\", "-", ":", " -", "*", "", "?", "", "\"", "'", "<", "", ">", "", "|", "-")

func sanitize(part string) string {
	s := strings.TrimSpace(unsafeReplacer.Replace(part))
	// A component that is empty or all dots (".", "..") would either vanish or
	// traverse up once path-joined — neutralize it so a book can never land
	// outside its library root, whatever a provider or manual edit supplies.
	if s == "" || strings.Trim(s, ".") == "" {
		return "_"
	}
	return s
}

// RenderName expands the naming scheme for a book. The result is a relative
// path without extension.
func RenderName(scheme string, b *catalog.Book) string {
	if strings.TrimSpace(scheme) == "" {
		scheme = "{Author}/{Title}"
	}
	year := ""
	if len(b.ReleaseDate) >= 4 {
		year = b.ReleaseDate[:4]
	}
	// token values are sanitized BEFORE substitution so a "/" inside a title
	// can never create an extra folder — only the scheme's own slashes split
	values := map[string]string{
		"Author":    sanitize(b.Author),
		"Title":     sanitize(b.Title),
		"Series":    sanitize(b.SeriesName),
		"SeriesNum": trimFloat(b.SeriesNum),
		"Year":      year,
	}
	out := scheme
	for token, v := range values {
		out = strings.ReplaceAll(out, "{"+token+"}", v)
	}
	parts := strings.Split(out, "/")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return filepath.Join(parts...)
}

func trimFloat(f float64) string {
	if f == 0 {
		return ""
	}
	s := fmt.Sprintf("%.1f", f)
	return strings.TrimSuffix(s, ".0")
}

// Deliver places srcPath into the library at rootPath for book, applying the
// naming scheme, writing metadata (epub only), recording the file, and
// hard-linking into any other library that has the same book without a file.
// Returns the file's final path in the primary library.
func (im *Importer) Deliver(bookID, libraryID int64, srcPath string, ns NamingSettings) (string, error) {
	book, err := im.Catalog.GetBook(bookID)
	if err != nil {
		return "", err
	}
	libs, err := im.Catalog.ListLibraries()
	if err != nil {
		return "", err
	}
	rootOf := map[int64]string{}
	for _, l := range libs {
		rootOf[l.ID] = l.RootPath
	}
	root, ok := rootOf[libraryID]
	if !ok {
		return "", fmt.Errorf("library %d not found", libraryID)
	}

	ext := strings.ToLower(filepath.Ext(srcPath))
	if ext == "" {
		ext = ".epub"
	}
	format := strings.TrimPrefix(ext, ".")

	// write metadata into the download BEFORE it lands, so every hardlink
	// shares the enriched copy
	if ns.WriteOnImport && ext == ".epub" {
		meta := bookToEpubMeta(book)
		meta.CoverPath = ns.CoverFile
		if err := epub.Write(srcPath, meta, ns.WriteFields); err != nil {
			// a malformed epub still deserves delivery; note and continue
			_ = im.Catalog.AddHistory(bookID, libraryID, "warning", "metadata write failed: "+err.Error())
		}
	}

	dst := filepath.Join(root, RenderName(ns.Scheme, book)+ext)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := moveOrCopy(srcPath, dst); err != nil {
		return "", err
	}
	info, err := os.Stat(dst)
	if err != nil {
		return "", err
	}
	if err := im.Catalog.SetFile(libraryID, bookID, dst, format, info.Size()); err != nil {
		return "", err
	}
	_ = im.Catalog.AddHistory(bookID, libraryID, "imported", dst)
	im.notifyFileAdded(bookID, libraryID, dst, format)

	// hardlink into every other library that wants this book and has no file
	rows, err := im.DB.Query(`SELECT library_id FROM library_books
		WHERE book_id = ? AND library_id != ? AND (file_path IS NULL OR file_path = '')`, bookID, libraryID)
	if err != nil {
		return dst, nil //nolint:nilerr // primary delivery succeeded; dedupe is best-effort
	}
	var others []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			others = append(others, id)
		}
	}
	rows.Close()
	for _, other := range others {
		otherRoot, ok := rootOf[other]
		if !ok {
			continue
		}
		link := filepath.Join(otherRoot, RenderName(ns.Scheme, book)+ext)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			continue
		}
		if err := linkOrCopy(dst, link); err != nil {
			_ = im.Catalog.AddHistory(bookID, other, "warning", "hardlink failed: "+err.Error())
			continue
		}
		if err := im.Catalog.SetFile(other, bookID, link, format, info.Size()); err != nil {
			continue
		}
		_ = im.Catalog.AddHistory(bookID, other, "imported", link+" (hardlink)")
		im.notifyFileAdded(bookID, other, link, format)
	}
	return dst, nil
}

func bookToEpubMeta(b *catalog.Book) epub.Metadata {
	return epub.Metadata{
		Title:       b.Title,
		Authors:     []string{b.Author},
		Description: b.Description,
		Language:    b.Language,
		Publisher:   b.Publisher,
		Date:        b.ReleaseDate,
		SeriesName:  b.SeriesName,
		SeriesIndex: b.SeriesNum,
		ISBN:        b.ISBN13,
		GoodreadsID: b.GoodreadsID,
		HardcoverID: b.HardcoverID,
	}
}

// moveOrCopy renames when possible (same filesystem) and falls back to copy.
func moveOrCopy(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// linkOrCopy hardlinks (the point of same-filesystem libraries) with a copy
// fallback for cross-device setups.
func linkOrCopy(src, dst string) error {
	_ = os.Remove(dst)
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), ".booky-cp-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), dst)
}

// FindBookFile locates the (largest) book file under path — which may be a
// finished download folder or already a file.
func FindBookFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return path, nil
	}
	var best string
	var bestSize int64
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !bookExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Size() > bestSize {
			best, bestSize = p, fi.Size()
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if best == "" {
		return "", fmt.Errorf("no book file found under %s", path)
	}
	return best, nil
}
