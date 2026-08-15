package watcher

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/metadata"
)

// Attach controls what ExpandAuthor does with the books it finds. The zero
// value means catalog-only: books appear on author and series pages but
// stay out of every library until the user monitors them. A LibraryID
// attaches them to that library with the given monitored flag; NewOnly
// limits attaching to books not previously known (the "new releases"
// author monitor mode — backlist stays catalog-only).
type Attach struct {
	LibraryID int64
	Monitored bool
	NewOnly   bool
}

// SyncBibliography pulls an author's bibliography and leaves every book it
// finds catalog-only: visible on the author page, in nobody's library until
// somebody shelves it.
//
// It used to consult a per-author "monitor mode" that could auto-attach new
// books to a library — and then had to guess which one. The guess is what let
// a sync drop books onto a shelf its owner never asked for, and no guess is
// better than a good one here: an unasked-for book on the author page costs
// nothing, while one on a shelf starts a download.
func (w *Watcher) SyncBibliography(ctx context.Context, authorID int64) (int, error) {
	return w.ExpandAuthor(ctx, authorID, Attach{})
}

// ExpandAuthor pulls an author's bibliography from the metadata providers
// and applies the attach options. Box sets and user-excluded titles never
// arrive; known books are matched, not duplicated; curated monitored flags
// are never touched. Returns how many books are new.
func (w *Watcher) ExpandAuthor(ctx context.Context, authorID int64, attach Attach) (int, error) {
	var name string
	if err := w.db.QueryRow(`SELECT name FROM authors WHERE id = ?`, authorID).Scan(&name); err != nil {
		return 0, fmt.Errorf("author %d not found", authorID)
	}
	works, err := w.chain.AuthorWorks(ctx, name, 100)
	if err != nil {
		return 0, fmt.Errorf("fetch works for %q: %w", name, err)
	}

	known, err := w.knownBookIDs(authorID)
	if err != nil {
		return 0, err
	}

	added := 0
	// track every row this sync touched, so untouched catalog-only leftovers
	// (foreign-language records, edition variants synced before the current
	// filters existed) can be pruned afterwards
	touched := make(map[int64]bool, len(works))
	upsertFailed := false
	for _, meta := range works {
		// drop works that aren't actually by this author — provider author
		// searches also surface anthologies and co-credits
		if len(meta.Authors) > 0 && !nameMatches(meta.Authors, name) {
			continue
		}
		// pin attribution to the author being expanded: co-author orderings
		// and name variants must never mint new author rows (that once
		// cascaded into an author explosion, each new row syncing its own
		// bibliography)
		meta.Authors = []string{name}
		// Pin to the concrete author id so a delete that lands mid-sync isn't
		// undone by recreating the author by name. If the author is gone, stop
		// the whole expansion — the remaining books would only resurrect it.
		bookID, err := w.catalog.UpsertBookForAuthor(meta, authorID)
		if errors.Is(err, catalog.ErrAuthorGone) {
			log.Printf("watcher: expand %q aborted: author %d deleted mid-sync", name, authorID)
			return added, nil
		}
		if err != nil {
			log.Printf("watcher: expand %q: %v", meta.Title, err)
			upsertFailed = true
			continue
		}
		touched[bookID] = true
		if w.userBlocked(bookID) {
			continue
		}
		isNew := !known[bookID]
		known[bookID] = true
		if isNew {
			added++
			if meta.CoverURL != "" {
				if err := w.covers.Ensure(ctx, bookID, meta.CoverURL); err != nil {
					log.Printf("watcher: expand cover %q: %v", meta.Title, err) // retries on refresh
				}
				// pace cover fetches — a bibliography can be ~100 covers
				if !w.pause(ctx, 150*time.Millisecond) {
					return added, ctx.Err()
				}
			}
		}
		if attach.LibraryID != 0 && (!attach.NewOnly || isNew) {
			if _, err := w.attachIfAbsent(bookID, attach.LibraryID, attach.Monitored); err != nil {
				log.Printf("watcher: expand attach %q: %v", meta.Title, err)
			}
		}
	}
	// The Goodreads series overlay adds announced books and unlinked novellas
	// to this author's watched series. Its rows join the touched set so the
	// prune below treats them as confirmed — they live exactly as long as
	// Goodreads still lists them.
	for id := range w.overlaySeries(ctx, authorID, name) {
		touched[id] = true
	}
	// books that landed in a MONITORED series join its library monitored —
	// monitoring a series means every book in it, including ones the sync
	// discovers later. Runs before the prune so the new memberships also
	// shield the rows.
	if n, err := w.catalog.AttachMonitoredSeriesBooks(authorID); err != nil {
		log.Printf("watcher: expand %q: attach monitored series books: %v", name, err)
	} else if n > 0 {
		log.Printf("watcher: expand %q: %d book(s) joined monitored series", name, n)
	}
	// The fresh bibliography is authoritative for catalog-only rows: anything
	// it no longer lists — foreign-language records and edition-variant dupes
	// synced before the current filters, works that turned out not to be this
	// author's — would otherwise sit on the author page forever, since expand
	// only ever adds. Books in a library, user-blocked, or user-edited are
	// never pruned; an empty or partially-failed sync prunes nothing.
	if len(works) > 0 && !upsertFailed {
		if pruned, err := w.pruneStaleCatalogBooks(authorID, touched); err != nil {
			log.Printf("watcher: expand %q: prune stale books: %v", name, err)
		} else if pruned > 0 {
			log.Printf("watcher: expand %q: pruned %d stale catalog book(s)", name, pruned)
		}
	}
	w.stampWorksSynced(authorID)
	w.syncAuthorInfo(ctx, authorID, name)
	if added > 0 {
		_ = w.catalog.AddHistory(0, attach.LibraryID, "backlist",
			fmt.Sprintf("%d new book(s) in %s's bibliography", added, name))
	}
	return added, nil
}

// syncAuthorInfo pulls the author's portrait + bio from Hardcover alongside
// the bibliography sync — at most monthly per author, one extra query. The
// photo file itself is fetched-and-cached lazily by the photo endpoint.
func (w *Watcher) syncAuthorInfo(ctx context.Context, authorID int64, name string) {
	if w.hardcover == nil || !w.hardcover.WorksConfigured() || !w.catalog.AuthorInfoDue(authorID) {
		return
	}
	info, err := w.hardcover.AuthorInfo(ctx, name)
	if err != nil {
		log.Printf("watcher: author info for %q: %v", name, err)
		return
	}
	if info == nil {
		// not on Hardcover — stamp anyway so the miss isn't retried every sync
		if err := w.catalog.SetAuthorInfo(authorID, "", ""); err != nil {
			log.Printf("watcher: save author info: %v", err)
		}
		return
	}
	if err := w.catalog.SetAuthorInfo(authorID, info.Bio, info.ImageURL); err != nil {
		log.Printf("watcher: save author info: %v", err)
	}
}

// pruneStaleCatalogBooks deletes the author's catalog-only books that the
// just-applied sync didn't confirm. Only rows with no library membership, no
// user block (the block must outlive the row to keep working) and no manual
// field edits qualify — everything a user touched survives.
func (w *Watcher) pruneStaleCatalogBooks(authorID int64, touched map[int64]bool) (int, error) {
	rows, err := w.db.Query(`SELECT b.id FROM books b
		WHERE b.author_id = ?
		  AND NOT EXISTS (SELECT 1 FROM library_books lb WHERE lb.book_id = b.id)
		  AND NOT EXISTS (SELECT 1 FROM blocklist bl WHERE bl.book_id = b.id AND bl.source = 'user')
		  AND (b.field_locks IS NULL OR b.field_locks = '' OR b.field_locks = '{}')`, authorID)
	if err != nil {
		return 0, err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if !touched[id] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	pruned := 0
	for _, id := range stale {
		if _, err := w.db.Exec(`DELETE FROM books WHERE id = ?`, id); err != nil {
			return pruned, err
		}
		// the cached cover dies with the row: SQLite reuses freed ids, and a
		// surviving file would show up as the next occupant's cover
		if w.covers != nil {
			_ = w.covers.Remove(id)
		}
		pruned++
	}
	return pruned, nil
}

// nameMatches reports whether target appears in the credited authors,
// tolerating punctuation/case differences ("R.K. Marsh" vs "R. K.
// Marsh").
func nameMatches(authors []string, target string) bool {
	want := metadata.NormalizeName(target)
	for _, a := range authors {
		if metadata.NormalizeName(a) == want {
			return true
		}
	}
	return false
}

// knownBookIDs returns the ids of the author's existing books, so expand can
// count genuinely new arrivals.
func (w *Watcher) knownBookIDs(authorID int64) (map[int64]bool, error) {
	rows, err := w.db.Query(`SELECT id FROM books WHERE author_id = ?`, authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// attachIfAbsent adds the book to the library only when it isn't already a
// member, reporting whether a new membership was created — an expand must
// never flip the monitored flag on books the user already curated.
func (w *Watcher) attachIfAbsent(bookID, libraryID int64, monitored bool) (bool, error) {
	var n int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM library_books WHERE library_id = ? AND book_id = ?`,
		libraryID, bookID).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if err := w.catalog.AddToLibrary(bookID, libraryID, monitored); err != nil {
		return false, err
	}
	return true, nil
}

func (w *Watcher) stampWorksSynced(authorID int64) {
	if _, err := w.db.Exec(`UPDATE authors SET works_synced_at = datetime('now') WHERE id = ?`, authorID); err != nil {
		log.Printf("watcher: stamp works sync: %v", err)
	}
}

// syncBibliographies keeps author pages populated without anyone pressing a
// button: each tick handles at most ONE author — new authors first, then
// monitored authors whose last sync is over a week old (so new releases
// appear on their own). One provider request per author, so the upstream
// APIs see at most a few requests per minute even right after a big import.
func (w *Watcher) syncBibliographies(ctx context.Context) {
	// only authors the user actually has: monitored, or with a book in some
	// library. Stray rows (old co-author fallout, removed authors' leftovers)
	// never trigger provider traffic.
	rows, err := w.db.Query(`
		SELECT a.id FROM authors a
		WHERE (a.works_synced_at IS NULL
		   OR (a.monitored = 1 AND a.works_synced_at < datetime('now', '-7 days')))
		  AND (a.monitored = 1 OR EXISTS (
			SELECT 1 FROM books b JOIN library_books lb ON lb.book_id = b.id
			WHERE b.author_id = a.id))
		ORDER BY (a.works_synced_at IS NULL) DESC, a.id
		LIMIT 25`)
	if err != nil {
		return
	}
	var candidates []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return
		}
		candidates = append(candidates, id)
	}
	rows.Close()

	// first candidate not sitting in the failure backoff; a flaky author
	// waits an hour while everyone behind it still gets synced
	var pick int64
	for _, id := range candidates {
		if until, bad := w.syncFail[id]; !bad || time.Now().After(until) {
			pick = id
			break
		}
	}
	if pick == 0 {
		return // nobody due (or everybody due is backing off)
	}
	added, err := w.ExpandAuthor(ctx, pick, Attach{})
	if err != nil {
		// Leave works_synced_at NULL and back off instead of stamping: the
		// old stamp-on-error permanently disqualified unmonitored authors
		// (nothing ever retried them), so a rate-limit blip during a big
		// library scan left author pages empty until a manual refresh.
		w.syncFail[pick] = time.Now().Add(time.Hour)
		log.Printf("watcher: bibliography sync author %d: %v (retrying in 1h)", pick, err)
		return
	}
	delete(w.syncFail, pick)
	if added > 0 {
		log.Printf("watcher: bibliography sync: %d new book(s) for author %d", added, pick)
	}
}
