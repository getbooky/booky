package acquire

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/epub"
	"github.com/getbooky/booky/internal/importer"
	"github.com/getbooky/booky/internal/release"
)

// NamingSettings exposes the import-time settings assembly for API-driven
// imports (manual import) so they name and enrich files exactly like
// automatic ones.
func (e *Engine) NamingSettings(bookID int64) importer.NamingSettings {
	return e.namingSettings(bookID)
}

// namingSettings assembles the import-time naming/metadata settings for a book.
func (e *Engine) namingSettings(bookID int64) importer.NamingSettings {
	fields := epub.Fields{}
	for _, f := range strings.Split(e.settings.Get("metadata_write"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			fields[f] = true
		}
	}
	return importer.NamingSettings{
		Scheme:        e.settings.Get("naming_scheme"),
		WriteOnImport: e.settings.Get("write_on_import") != "false",
		WriteFields:   fields,
		CoverFile:     e.trustedCover(bookID),
	}
}

// trustedCover returns the cached cover file only when it's safe to bake into
// the delivered epub. When Hardcover is configured, a book that never resolved
// to a Hardcover identity is carrying an unverified feed cover (Goodreads RSS
// covers are frequently the wrong edition or a placeholder) — showing it in
// the UI is recoverable, but writing it into the file is not, so it stays out.
func (e *Engine) trustedCover(bookID int64) string {
	cover := e.covers.Path(bookID)
	if cover == "" || e.settings.Get("hardcover_token") == "" {
		return cover
	}
	if b, err := e.catalog.GetBook(bookID); err == nil && b.HardcoverID == "" {
		return ""
	}
	return cover
}

// DeliverPending imports every finished direct download waiting in the queue.
func (e *Engine) DeliverPending() {
	pending, err := e.PendingImports()
	if err != nil {
		return
	}
	for _, it := range pending {
		_, err := e.importer.Deliver(it.BookID, it.LibraryID, it.ExternalID, e.namingSettings(it.BookID))
		if err != nil {
			e.markImportFailed(it, err)
			continue
		}
		e.MarkDone(it.ID)
	}
}

// markImportFailed records a failed import in the queue AND the history feed
// — the file arrived but couldn't be delivered (permissions, disk, bad
// archive), which is a local problem: no blocklist, no cascade, but the
// reason must be visible in Activity. The distinct status is what tells the
// UI "there's a file on disk worth retrying" — download failures stay
// plain "failed", where retrying an import would make no sense.
func (e *Engine) markImportFailed(it QueueItem, cause error) {
	e.setQueue(it.ID, "import_failed", cause.Error())
	_ = e.catalog.AddHistory(it.BookID, it.LibraryID, "import failed", it.ReleaseTitle+": "+cause.Error())
}

// PollSab imports completed SABnzbd jobs matching queue rows.
func (e *Engine) PollSab(ctx context.Context) {
	sab := e.Sab()
	if !sab.Configured() {
		return
	}
	hist, err := sab.History(ctx)
	if err != nil {
		return
	}
	byNzo := map[string]struct{ status, storage, fail string }{}
	for _, h := range hist {
		byNzo[h.ID] = struct{ status, storage, fail string }{h.Status, h.Storage, h.Fail}
	}
	items, err := e.Queue(nil)
	if err != nil {
		return
	}
	for _, it := range items {
		if it.Status != "downloading" || it.Protocol != "usenet" {
			continue
		}
		h, ok := byNzo[it.ExternalID]
		if !ok {
			continue
		}
		switch h.status {
		case "Completed":
			path, err := importer.FindBookFile(h.storage)
			if err != nil {
				// downloaded but no usable book inside — that's a verdict on
				// the release, so block it and move down the list
				e.failAndCascade(ctx, it, "no book file in "+h.storage)
				continue
			}
			_, err = e.importer.Deliver(it.BookID, it.LibraryID, path, e.namingSettings(it.BookID))
			if err != nil {
				// import errors are usually local (permissions, disk) — keep
				// the job for inspection instead of burning the release
				e.markImportFailed(it, err)
				continue
			}
			e.MarkDone(it.ID)
			// the book is in the library; clear the job and its leftover
			// folder from SAB so completed downloads don't pile up. Failed
			// or unimportable jobs stay for inspection.
			if err := sab.Delete(ctx, it.ExternalID, true); err != nil {
				log.Printf("acquire: cleanup sab job %s: %v", it.ExternalID, err)
			}
		case "Failed":
			e.failAndCascade(ctx, it, h.fail)
		}
	}
}

// failAndCascade handles a download that died after the grab: the queue row
// is failed, the release blocklisted (this copy is bad — never fetch it
// again), and the next-best candidate grabbed automatically, so one dead
// release walks down the ranked list instead of stranding the book.
func (e *Engine) failAndCascade(ctx context.Context, it QueueItem, reason string) {
	e.MarkFailed(it.ID, reason)
	if err := e.Block(it.BookID, release.Release{Title: it.ReleaseTitle, Source: it.Source}, reason); err != nil {
		log.Printf("acquire: blocklist %q: %v", it.ReleaseTitle, err)
	}
	_ = e.catalog.AddHistory(it.BookID, it.LibraryID, "failed", it.ReleaseTitle+": "+reason)
	grabbed, err := e.AutoGrab(ctx, it.BookID, it.LibraryID, 0)
	switch {
	case err != nil:
		log.Printf("acquire: cascade after %q: %v", it.ReleaseTitle, err)
	case grabbed:
		log.Printf("acquire: %q failed, grabbed next candidate", it.ReleaseTitle)
	default:
		log.Printf("acquire: %q failed, no further candidates", it.ReleaseTitle)
	}
}

// AutoGrab searches for a book and grabs the best acceptable candidate,
// falling through to the next when a grab fails (failures blocklist
// themselves inside Grab). Only positively-scored releases qualify — an
// automatic pass never takes a format the profile rejects; that choice is
// reserved for a human force-grab. Returns whether something was grabbed.
func (e *Engine) AutoGrab(ctx context.Context, bookID, libraryID, profileID int64) (bool, error) {
	// One grab in flight per book+library. Two list entries converging on the
	// same book, or a monitor toggle racing a list poll, would otherwise
	// download the same book twice — report "grabbed" so callers don't
	// escalate; the running grab IS the outcome.
	var active int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM queue
		WHERE book_id = ? AND library_id = ? AND status IN ('queued', 'downloading', 'importing')`,
		bookID, libraryID).Scan(&active); err == nil && active > 0 {
		log.Printf("acquire: auto-grab skipped — a grab for book %d is already in flight", bookID)
		return true, nil
	}
	rels, err := e.SearchWithProfile(ctx, bookID, libraryID, profileID)
	if err != nil {
		return false, err
	}
	for _, rel := range rels {
		if rel.Score <= 0 {
			continue
		}
		if _, err := e.Grab(ctx, bookID, libraryID, rel); err != nil {
			log.Printf("acquire: auto-grab %q failed, trying next: %v", rel.Title, err)
			continue
		}
		return true, nil
	}
	return false, nil
}

// CutoffUnmet lists books that have a file, but in a worse format than their
// profile's cutoff — the upgrade list the backlog pass works through.
func (e *Engine) CutoffUnmet() ([]catalog.Book, []int64, error) {
	rows, err := e.db.Query(`
		SELECT b.id, lb.library_id, lb.file_format, p.formats, p.cutoff_format
		FROM library_books lb
		JOIN books b ON b.id = lb.book_id
		JOIN libraries l ON l.id = lb.library_id
		JOIN quality_profiles p ON p.id = l.quality_profile_id
		WHERE lb.monitored = 1 AND lb.file_path IS NOT NULL AND lb.file_format != ''`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	type row struct {
		bookID, libraryID       int64
		format, formats, cutoff string
	}
	var candidates []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.bookID, &r.libraryID, &r.format, &r.formats, &r.cutoff); err != nil {
			return nil, nil, err
		}
		candidates = append(candidates, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var books []catalog.Book
	var libIDs []int64
	for _, r := range candidates {
		var formats []string
		if err := json.Unmarshal([]byte(r.formats), &formats); err != nil {
			continue
		}
		if formatRank(formats, r.format) <= formatRank(formats, r.cutoff) {
			continue // at or above cutoff (or cutoff unknown)
		}
		b, err := e.catalog.GetBook(r.bookID)
		if err != nil {
			continue
		}
		b.LibraryID = r.libraryID
		b.Monitored = true
		b.FileFormat = r.format
		books = append(books, *b)
		libIDs = append(libIDs, r.libraryID)
	}
	return books, libIDs, nil
}

// formatRank returns the index of format in the profile order, or a rank
// worse than any listed format when it's absent.
func formatRank(formats []string, format string) int {
	for i, f := range formats {
		if strings.EqualFold(f, format) {
			return i
		}
	}
	return len(formats)
}
