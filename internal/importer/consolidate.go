package importer

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// DuplicatesDir is where consolidation sets aside a second copy of a book
// that differs from the one kept — never deleted, for a person to look at.
// The scanner skips it (hidden folders hold bookkeeping, not books).
const DuplicatesDir = ".booky-duplicates"

// ConsolidateReport says what one consolidation pass did.
type ConsolidateReport struct {
	Moved        int // kept files moved into the library root
	Removed      int // duplicate copies removed: hardlinks or byte-identical
	SetAside     int // differing duplicates moved into DuplicatesDir
	Failed       int
	PrunedDirs   int
	FirstFailure string
}

// Consolidate finishes the move to one library that migration 0024 started
// in the database: every kept file that still sits outside the library root
// moves into it under the naming scheme, and every other copy of the same
// book (recorded in library_merge) is resolved without ever losing content:
//
//   - a hardlink to the kept file, or a byte-identical copy, is removed;
//   - anything else is moved into DuplicatesDir for a person to look at.
//
// Folders left empty behind moved files are pruned (only empty ones — a
// folder with anything left in it is never touched). Safe to run on every
// start: once everything is home it finds nothing to do.
func (im *Importer) Consolidate(scheme string) (*ConsolidateReport, error) {
	rep := &ConsolidateReport{}
	libID := im.Catalog.TheLibrary()
	if libID == 0 {
		return rep, nil
	}
	var root string
	if err := im.DB.QueryRow(`SELECT root_path FROM libraries WHERE id = ?`, libID).Scan(&root); err != nil {
		return rep, err
	}
	root = filepath.Clean(root)
	fail := func(err error) {
		rep.Failed++
		if rep.FirstFailure == "" {
			rep.FirstFailure = err.Error()
		}
		log.Printf("consolidate: %v", err)
	}
	var emptied []string

	// 1. kept files outside the root move home
	type kept struct {
		bookID int64
		path   string
		format string
		size   int64
	}
	rows, err := im.DB.Query(`SELECT book_id, file_path, COALESCE(file_format, ''), COALESCE(file_size, 0)
		FROM library_books WHERE library_id = ? AND file_path IS NOT NULL AND file_path != ''`, libID)
	if err != nil {
		return rep, err
	}
	var outside []kept
	for rows.Next() {
		var k kept
		if err := rows.Scan(&k.bookID, &k.path, &k.format, &k.size); err != nil {
			rows.Close()
			return rep, err
		}
		if !within(root, k.path) {
			outside = append(outside, k)
		}
	}
	rows.Close()
	for _, k := range outside {
		if _, err := os.Stat(k.path); err != nil {
			continue // already gone; nothing to move
		}
		book, err := im.Catalog.GetBook(k.bookID)
		if err != nil {
			fail(fmt.Errorf("book %d: %w", k.bookID, err))
			continue
		}
		dst := freePath(filepath.Join(root, RenderName(scheme, book)+strings.ToLower(filepath.Ext(k.path))), k.path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fail(err)
			continue
		}
		if err := moveOrCopy(k.path, dst); err != nil {
			fail(fmt.Errorf("move %s: %w", k.path, err))
			continue
		}
		if err := im.Catalog.SetFile(libID, k.bookID, dst, k.format, k.size); err != nil {
			fail(err)
			continue
		}
		_ = im.Catalog.AddHistory(k.bookID, libID, "moved", fmt.Sprintf("%s → %s (merged into one library)", k.path, dst))
		emptied = append(emptied, filepath.Dir(k.path))
		rep.Moved++
	}

	// 2. the other copies
	type dup struct {
		rowid  int64
		bookID int64
		path   string
	}
	rows, err = im.DB.Query(`SELECT rowid, book_id, file_path FROM library_merge WHERE done = 0`)
	if err != nil {
		return rep, err
	}
	var dups []dup
	for rows.Next() {
		var d dup
		if err := rows.Scan(&d.rowid, &d.bookID, &d.path); err != nil {
			rows.Close()
			return rep, err
		}
		dups = append(dups, d)
	}
	rows.Close()
	for _, d := range dups {
		var keptPath string
		_ = im.DB.QueryRow(`SELECT COALESCE(file_path, '') FROM library_books WHERE book_id = ?`, d.bookID).Scan(&keptPath)
		done := func() { _, _ = im.DB.Exec(`UPDATE library_merge SET done = 1 WHERE rowid = ?`, d.rowid) }
		if _, err := os.Lstat(d.path); err != nil || filepath.Clean(d.path) == filepath.Clean(keptPath) {
			done()
			continue
		}
		if keptPath != "" && sameContent(d.path, keptPath) {
			if err := os.Remove(d.path); err != nil {
				fail(err)
				continue
			}
			emptied = append(emptied, filepath.Dir(d.path))
			rep.Removed++
			done()
			continue
		}
		// a different copy (another edition, a re-download): keep it, aside
		rel := filepath.Base(d.path)
		if book, err := im.Catalog.GetBook(d.bookID); err == nil {
			rel = RenderName(scheme, book) + strings.ToLower(filepath.Ext(d.path))
		}
		dst := freePath(filepath.Join(root, DuplicatesDir, rel), d.path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fail(err)
			continue
		}
		if err := moveOrCopy(d.path, dst); err != nil {
			fail(fmt.Errorf("set aside %s: %w", d.path, err))
			continue
		}
		_ = im.Catalog.AddHistory(d.bookID, libID, "warning",
			fmt.Sprintf("a second, different copy was set aside while merging libraries: %s", dst))
		emptied = append(emptied, filepath.Dir(d.path))
		rep.SetAside++
		done()
	}

	// 3. prune folders the moves left empty, walking up while they stay empty
	// (os.Remove refuses a non-empty directory, which is the whole safeguard)
	for _, dir := range emptied {
		for dir != "/" && dir != "." && !within(dir, root) {
			if err := os.Remove(dir); err != nil {
				break
			}
			rep.PrunedDirs++
			dir = filepath.Dir(dir)
		}
	}
	return rep, nil
}

// within reports whether path sits inside (or is) dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// freePath returns want, or want with " (2)", " (3)"… inserted before the
// extension when another file already sits there. src itself never counts as
// a clash.
func freePath(want, src string) string {
	ext := filepath.Ext(want)
	base := strings.TrimSuffix(want, ext)
	p := want
	for i := 2; ; i++ {
		if _, err := os.Lstat(p); err != nil || filepath.Clean(p) == filepath.Clean(src) {
			return p
		}
		p = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}

// sameContent reports whether two paths hold the same bytes: the same inode
// (a hardlink — the usual case, since imports hard-linked across libraries) or
// byte-identical files.
func sameContent(a, b string) bool {
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return false
	}
	if os.SameFile(ia, ib) {
		return true
	}
	if ia.Size() != ib.Size() {
		return false
	}
	ha, err1 := fileHash(a)
	hb, err2 := fileHash(b)
	return err1 == nil && err2 == nil && bytes.Equal(ha, hb)
}

func fileHash(p string) ([]byte, error) {
	f, err := os.Open(p) //nolint:gosec // paths come from library_books / library_merge
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
