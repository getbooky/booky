// Package importer scans a library's root folder and matches the files
// already on disk — strictly in place, nothing is moved or renamed.
//
// Match ladder, most to least certain:
//  1. embedded Booky/calibre identifiers (Goodreads ID, Hardcover ID, ISBN)
//  2. embedded title+author metadata → provider lookup, fuzzy-verified
//  3. filename guess → review queue for the user
package importer

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/epub"
	"github.com/getbooky/booky/internal/metadata"
)

var bookExts = map[string]bool{".epub": true, ".pdf": true, ".mobi": true, ".azw3": true}

type Importer struct {
	DB      *sql.DB
	Catalog *catalog.Store
	Chain   *metadata.Chain
	// Covers, when set, caches each matched book's cover at import time so
	// scanned-in books look right without a manual refresh.
	Covers *catalog.CoverCache
	// AutoMatchThreshold: candidates scoring at or above are matched without
	// review; below lands in the review queue.
	AutoMatchThreshold float64
}

func New(db *sql.DB, cat *catalog.Store, chain *metadata.Chain) *Importer {
	return &Importer{DB: db, Catalog: cat, Chain: chain, AutoMatchThreshold: 85}
}

type ScanResult struct {
	Scanned int `json:"scanned"`
	Matched int `json:"matched"`
	Review  int `json:"review"`
	Skipped int `json:"skipped"`
}

// Scan walks the library's root folder and matches every book file.
func (im *Importer) Scan(ctx context.Context, libraryID int64, rootPath string) (*ScanResult, error) {
	result := &ScanResult{}
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() || !bookExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		result.Scanned++

		info, _ := d.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}

		// already imported at this path? skip quickly
		var existing int
		if err := im.DB.QueryRow(`SELECT COUNT(*) FROM library_books WHERE library_id = ? AND file_path = ?`,
			libraryID, path).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			result.Matched++
			return nil
		}

		matched, err := im.matchFile(ctx, libraryID, path, size)
		if err != nil {
			log.Printf("import: %s: %v", path, err)
			result.Skipped++
			return nil
		}
		if matched {
			result.Matched++
		} else {
			result.Review++
		}
		return nil
	})
	return result, err
}

func (im *Importer) matchFile(ctx context.Context, libraryID int64, path string, size int64) (bool, error) {
	var meta metadata.BookMeta
	haveEmbedded := false

	if strings.EqualFold(filepath.Ext(path), ".epub") {
		if em, err := epub.Read(path); err == nil && em.Title != "" {
			haveEmbedded = true
			meta = metadata.BookMeta{
				Provider:    "file",
				Title:       em.Title,
				Authors:     em.Authors,
				Description: em.Description,
				Language:    em.Language,
				Publisher:   em.Publisher,
				SeriesName:  em.SeriesName,
				SeriesIndex: em.SeriesIndex,
				GoodreadsID: em.GoodreadsID,
				HardcoverID: em.HardcoverID,
			}
			if len(em.ISBN) == 13 {
				meta.ISBN13 = em.ISBN
			}
		}
	}

	// rung 1: embedded identifiers are trusted outright — but the catalog
	// record still goes through the metadata pipeline: enriching resolves the
	// identifier against the providers, so a scanned-in book gets the same
	// canonical title, series, description and cover as any other add
	// instead of whatever its file happened to embed.
	if meta.GoodreadsID != "" || meta.HardcoverID != "" || meta.ISBN13 != "" {
		if im.Chain != nil {
			meta = im.Chain.Enrich(ctx, meta)
		}
		return true, im.commitMatch(ctx, libraryID, path, size, meta)
	}

	// rung 2: embedded title/author → provider lookup, fuzzy-verified
	if haveEmbedded && im.Chain != nil {
		params := metadata.SearchParams{Title: meta.Title, Limit: 3}
		if len(meta.Authors) > 0 {
			params.Author = meta.Authors[0]
		}
		if results, err := im.Chain.Search(ctx, params); err == nil {
			for _, candidate := range results {
				author := params.Author
				if score := scoreFor(candidate, meta.Title, author); score >= im.AutoMatchThreshold {
					metadata.Merge(&candidate, meta)
					// search hits are shallow — enrich fills description,
					// series, genres and the cover before committing
					candidate = im.Chain.Enrich(ctx, candidate)
					return true, im.commitMatch(ctx, libraryID, path, size, candidate)
				}
			}
		}
	}

	// rung 3: queue for review with the best filename guess we can make
	title, author := guessFromFilename(path)
	if haveEmbedded {
		title = meta.Title
		if len(meta.Authors) > 0 {
			author = meta.Authors[0]
		}
	}
	_, err := im.DB.Exec(`INSERT INTO import_files (library_id, path, size, status, guess_title, guess_author)
		VALUES (?, ?, ?, 'review', ?, ?)
		ON CONFLICT(library_id, path) DO UPDATE SET guess_title = excluded.guess_title, guess_author = excluded.guess_author`,
		libraryID, path, size, title, author)
	return false, err
}

func (im *Importer) commitMatch(ctx context.Context, libraryID int64, path string, size int64, meta metadata.BookMeta) error {
	bookID, err := im.Catalog.UpsertBook(meta)
	if err != nil {
		return err
	}
	if err := im.Catalog.AddToLibrary(bookID, libraryID, true); err != nil {
		return err
	}
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if err := im.Catalog.SetFile(libraryID, bookID, path, format, size); err != nil {
		return err
	}
	if im.Covers != nil && meta.CoverURL != "" {
		if err := im.Covers.Ensure(ctx, bookID, meta.CoverURL); err != nil {
			log.Printf("import: cover for %q: %v", meta.Title, err) // retries on refresh
		}
	}
	// clear any stale review row for this path
	_, err = im.DB.Exec(`DELETE FROM import_files WHERE library_id = ? AND path = ?`, libraryID, path)
	if err != nil {
		return err
	}
	return im.Catalog.AddHistory(bookID, libraryID, "imported", "matched in place: "+filepath.Base(path))
}

// scoreFor mirrors the metadata ranking for match verification.
func scoreFor(candidate metadata.BookMeta, title, author string) float64 {
	return metadata.ScoreCandidate(candidate, title, author)
}

var fileJunk = regexp.MustCompile(`(?i)[\[(].*?[\])]|\b(retail|epub|mobi|azw3|pdf|v\d+(\.\d+)*|final\d*)\b|_+`)

// guessFromFilename turns "tess arden - vault 1 (v5, retail).epub" into a
// title/author guess for the review queue.
func guessFromFilename(path string) (title, author string) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	// underscores are word chars to the regex engine, so \b-anchored junk
	// patterns can't fire through them — space them out first
	base = strings.ReplaceAll(base, "_", " ")
	base = fileJunk.ReplaceAllString(base, " ")
	base = strings.Join(strings.Fields(base), " ")
	if parts := strings.SplitN(base, " - ", 2); len(parts) == 2 {
		return strings.TrimSpace(parts[1]), strings.TrimSpace(parts[0])
	}
	return base, ""
}

type ReviewItem struct {
	ID          int64   `json:"id"`
	Path        string  `json:"path"`
	GuessTitle  string  `json:"guessTitle"`
	GuessAuthor string  `json:"guessAuthor"`
	Confidence  float64 `json:"confidence"`
}

func (im *Importer) ReviewQueue(libraryID int64) ([]ReviewItem, error) {
	rows, err := im.DB.Query(`SELECT id, path, guess_title, guess_author, confidence
		FROM import_files WHERE library_id = ? AND status = 'review' ORDER BY path`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewItem
	for rows.Next() {
		var r ReviewItem
		if err := rows.Scan(&r.ID, &r.Path, &r.GuessTitle, &r.GuessAuthor, &r.Confidence); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (im *Importer) Ignore(fileID int64) error {
	_, err := im.DB.Exec(`UPDATE import_files SET status = 'ignored' WHERE id = ?`, fileID)
	return err
}

// Accept resolves a review-queue file to the user's chosen book.
func (im *Importer) Accept(fileID int64, meta metadata.BookMeta) (int64, error) {
	var libraryID int64
	var path string
	var size int64
	err := im.DB.QueryRow(`SELECT library_id, path, size FROM import_files WHERE id = ? AND status = 'review'`,
		fileID).Scan(&libraryID, &path, &size)
	if err != nil {
		return 0, fmt.Errorf("review file %d: %w", fileID, err)
	}
	if err := im.commitMatch(context.Background(), libraryID, path, size, meta); err != nil {
		return 0, err
	}
	var bookID int64
	err = im.DB.QueryRow(`SELECT book_id FROM library_books WHERE library_id = ? AND file_path = ?`,
		libraryID, path).Scan(&bookID)
	return bookID, err
}
