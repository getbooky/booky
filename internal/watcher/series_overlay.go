package watcher

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/getbooky/booky/internal/metadata"
)

// The Goodreads series overlay fills the one gap Hardcover leaves in series
// pages: announced-but-unreleased books (known years ahead on Goodreads) and
// the occasional novella Hardcover hasn't linked. It runs inside the
// bibliography sync, AFTER the Hardcover works land and BEFORE the stale
// prune — overlay rows join the sync's touched set, so they live exactly as
// long as Goodreads still lists them.
//
// Goodreads series pages also carry box sets, split translations and foreign
// editions; the overlay is deliberately narrow about what it admits:
//   - clean numeric position only ("1", "3.5" — never "1-3" or "5, part 1"),
//     and no existing series entry at that position
//   - the credited author must be the author being synced
//   - no compilations, no non-Latin-script titles
//   - a real readership (>=100 ratings), or an announced book due within
//     the horizon (this year or next — announced books legitimately have
//     zero ratings, but a title three years out is noise, not signal; it
//     ages into the window on a later sync)

const (
	overlayMinRatings   = 100
	overlayMaxPerSeries = 8
	overlayHorizonYears = 1
)

// overlaySeries returns the ids of books it added or matched so the caller
// can shield them from the stale prune. Failures are logged and skipped —
// scraping must never sink a bibliography sync.
func (w *Watcher) overlaySeries(ctx context.Context, authorID int64, authorName string) map[int64]bool {
	touched := map[int64]bool{}
	// off (the default): no fetches, and nothing confirms previously added
	// overlay rows — the stale prune clears them on each author's next sync
	if w.GRSeries == nil || w.settings.Get("series_overlay") != "true" {
		return touched
	}
	rows, err := w.db.Query(`
		SELECT sr.id, sr.name, COALESCE(sr.goodreads_id, ''),
		       COALESCE((SELECT b.goodreads_id FROM books b
		                 WHERE b.series_id = sr.id AND b.goodreads_id IS NOT NULL AND b.goodreads_id != ''
		                 LIMIT 1), '')
		FROM series sr
		WHERE sr.author_id = ?
		  AND (sr.monitored = 1 OR EXISTS (
			SELECT 1 FROM books b JOIN library_books lb ON lb.book_id = b.id
			WHERE b.series_id = sr.id))`, authorID)
	if err != nil {
		log.Printf("watcher: series overlay for %q: %v", authorName, err)
		return touched
	}
	type target struct {
		id           int64
		name, grID   string
		memberBookID string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.name, &t.grID, &t.memberBookID); err != nil {
			rows.Close()
			return touched
		}
		targets = append(targets, t)
	}
	rows.Close()

	for _, t := range targets {
		if t.grID == "" {
			t.grID = w.discoverSeriesID(ctx, t.name, t.memberBookID)
			if t.grID == "" {
				continue
			}
			if _, err := w.db.Exec(`UPDATE series SET goodreads_id = ? WHERE id = ?`, t.grID, t.id); err != nil {
				log.Printf("watcher: series overlay: save gr id for %q: %v", t.name, err)
			}
		}
		if !w.pause(ctx, w.GRSeries.Delay) {
			return touched
		}
		entries, err := w.GRSeries.SeriesEntries(ctx, t.grID)
		if err != nil {
			log.Printf("watcher: series overlay %q: %v", t.name, err)
			continue
		}
		w.applySeriesOverlay(ctx, authorID, authorName, t.id, t.name, entries, touched)
	}
	return touched
}

// discoverSeriesID maps a Booky series to its Goodreads series id via any
// member book's Goodreads page. The Goodreads series name must match ours —
// a book's first-listed series can be a different grouping entirely.
func (w *Watcher) discoverSeriesID(ctx context.Context, seriesName, memberBookID string) string {
	if memberBookID == "" {
		return ""
	}
	grName, grID, _, err := w.GRSeries.SeriesRefForBook(ctx, memberBookID)
	if err != nil || grID == "" {
		return ""
	}
	if !seriesNamesMatch(grName, seriesName) {
		return ""
	}
	return grID
}

// seriesNamesMatch compares loosely: "Iron Dawn Saga" and "Iron Dawn"
// style prefix drift is tolerated, unrelated names are not.
func seriesNamesMatch(a, b string) bool {
	na, nb := metadata.NormalizeName(a), metadata.NormalizeName(b)
	if na == "" || nb == "" {
		return false
	}
	return na == nb || strings.HasPrefix(na, nb) || strings.HasPrefix(nb, na)
}

func (w *Watcher) applySeriesOverlay(ctx context.Context, authorID int64, authorName string,
	seriesID int64, seriesName string, entries []metadata.GRSeriesEntry, touched map[int64]bool) {

	// existing occupancy: positions and normalized titles already in the series
	positions := map[string]int64{}
	titles := map[string]int64{}
	rows, err := w.db.Query(`SELECT id, title, COALESCE(series_num, 0) FROM books WHERE series_id = ?`, seriesID)
	if err != nil {
		return
	}
	for rows.Next() {
		var id int64
		var title string
		var num float64
		if err := rows.Scan(&id, &title, &num); err != nil {
			rows.Close()
			return
		}
		positions[fmt.Sprintf("%g", num)] = id
		titles[metadata.NormalizeName(title)] = id
	}
	rows.Close()

	thisYear := time.Now().UTC().Year()
	added := 0
	for _, e := range entries {
		if added >= overlayMaxPerSeries {
			log.Printf("watcher: series overlay %q: add cap reached, rest skipped", seriesName)
			return
		}
		if e.Index <= 0 || metadata.Excluded(e.Title, w.settings.ExcludePatterns()) || !latinTitle(e.Title) {
			continue
		}
		if !nameMatches([]string{e.Author}, authorName) {
			continue
		}
		year, yearErr := strconv.Atoi(e.Year)
		dueSoon := yearErr == nil && year >= thisYear && year <= thisYear+overlayHorizonYears
		if e.RatingsCount < overlayMinRatings && !dueSoon {
			continue
		}
		// an entry Goodreads still lists confirms whatever row occupies its
		// slot — mark it touched so the stale prune keeps overlay rows alive
		// across syncs
		if id, taken := positions[fmt.Sprintf("%g", e.Index)]; taken {
			touched[id] = true
			continue
		}
		if id, known := titles[metadata.NormalizeName(e.Title)]; known {
			touched[id] = true // already present without a position — don't duplicate
			continue
		}
		meta := w.enrichOverlayEntry(ctx, e, authorName, seriesName)
		bookID, err := w.catalog.UpsertBookForAuthor(meta, authorID)
		if err != nil {
			log.Printf("watcher: series overlay add %q: %v", e.Title, err)
			continue
		}
		touched[bookID] = true
		positions[fmt.Sprintf("%g", e.Index)] = bookID
		titles[metadata.NormalizeName(e.Title)] = bookID
		added++
		log.Printf("watcher: series overlay %q: added %q (#%g, %s)", seriesName, e.Title, e.Index, orUnknown(e.Year))
		if meta.CoverURL != "" {
			if err := w.covers.Ensure(ctx, bookID, meta.CoverURL); err != nil {
				log.Printf("watcher: series overlay cover %q: %v", e.Title, err)
			}
		}
	}
}

// enrichOverlayEntry tries to resolve the Goodreads entry against the
// provider chain (Hardcover first) so an entry Hardcover DOES know arrives
// with canonical metadata; otherwise the Goodreads series data — title,
// position, year, description, cover — stands on its own.
func (w *Watcher) enrichOverlayEntry(ctx context.Context, e metadata.GRSeriesEntry, authorName, seriesName string) metadata.BookMeta {
	meta := metadata.BookMeta{
		Provider:     "goodreads",
		Title:        e.Title,
		Authors:      []string{authorName},
		SeriesName:   seriesName,
		SeriesIndex:  e.Index,
		GoodreadsID:  e.GoodreadsID,
		CoverURL:     e.CoverURL,
		ReleaseDate:  e.Year, // YYYY; enrichment upgrades to a full date when known
		Description:  e.Description,
		RatingsCount: e.RatingsCount,
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	candidates, err := w.chain.Search(cctx, metadata.SearchParams{Title: e.Title, Author: authorName, Limit: 5})
	if err != nil || len(candidates) == 0 {
		return meta
	}
	best, ok := metadata.BestConfidentMatch(candidates, e.Title, authorName)
	if !ok {
		return meta
	}
	// canonical identity and richer fields win; the series slot is ours
	best.Authors = []string{authorName}
	best.SeriesName = seriesName
	best.SeriesIndex = e.Index
	if best.GoodreadsID == "" {
		best.GoodreadsID = e.GoodreadsID
	}
	if best.Description == "" {
		best.Description = e.Description
	}
	if best.CoverURL == "" {
		best.CoverURL = e.CoverURL
	}
	if best.ReleaseDate == "" {
		best.ReleaseDate = e.Year
	}
	return best
}

// latinTitle rejects titles written in a non-Latin script (split
// translations and foreign editions on series pages); accented Latin is fine.
func latinTitle(s string) bool {
	letters, latin := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if unicode.In(r, unicode.Latin) {
				latin++
			}
		}
	}
	return letters == 0 || latin*10 >= letters*9
}

func orUnknown(s string) string {
	if s == "" {
		return "year unknown"
	}
	return s
}
