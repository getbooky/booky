package watcher

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/acquire"
	"github.com/getbooky/booky/internal/backup"
	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/metadata"
	"github.com/getbooky/booky/internal/settings"
)

// Watcher owns the background loops. Search fires only from here (list add,
// release day, backlog) or from explicit UI actions — never a free-running
// retry loop.
type Watcher struct {
	db        *sql.DB
	catalog   *catalog.Store
	chain     *metadata.Chain
	settings  *settings.Store
	engine    *acquire.Engine
	covers    *catalog.CoverCache
	goodreads *GoodreadsRSS
	hardcover *metadata.Hardcover
	// GRSeries scrapes Goodreads series pages for the bibliography sync's
	// series overlay (announced books, unlinked novellas). Exported so tests
	// can point BaseURL at a fixture server; nil disables the overlay.
	GRSeries *metadata.Goodreads
	// Backups, when set, are created on a weekly tick and pruned to the
	// configured keep count.
	Backups *backup.Manager
	// Pace spaces consecutive automatic searches (release-day taper, backlog)
	// so providers never see a burst. Zeroed in tests.
	Pace time.Duration
	// syncFail backs off authors whose last bibliography attempt errored, so
	// one flaky author can't wedge the sync queue while everyone else waits.
	// In-memory on purpose: a restart forgets it, which is exactly the retry
	// you want after a provider outage. Only the tick goroutine touches it.
	syncFail map[int64]time.Time
}

func New(db *sql.DB, cat *catalog.Store, chain *metadata.Chain, cfg *settings.Store,
	engine *acquire.Engine, covers *catalog.CoverCache, hc *metadata.Hardcover) *Watcher {
	return &Watcher{
		db: db, catalog: cat, chain: chain, settings: cfg,
		engine: engine, covers: covers,
		goodreads: NewGoodreadsRSS(), hardcover: hc,
		GRSeries: metadata.NewGoodreads(),
		Pace:     4 * time.Second,
		syncFail: map[int64]time.Time{},
	}
}

// pollInterval is the user's list poll interval, floored at 30s.
func (w *Watcher) pollInterval() time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(w.settings.Get("list_poll_seconds")))
	if err != nil || secs <= 0 {
		secs = 60
	}
	if secs < 30 {
		secs = 30
	}
	return time.Duration(secs) * time.Second
}

// Run ticks until ctx is cancelled. Each tick polls due lists, tracks
// downloads, and — on their own cadence — runs the release-day check, the
// weekly release-date refresh, and the weekly backlog pass.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	log.Printf("watcher: running (list poll interval %s)", w.pollInterval())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Watcher) tick(ctx context.Context) {
	// finished downloads import even when nobody has the UI open
	w.engine.PollSab(ctx)
	w.engine.DeliverPending()

	w.pollDueLists(ctx)
	w.syncBibliographies(ctx)

	if w.due("calendar_last_check", time.Hour) {
		w.setNow("calendar_last_check")
		w.releaseDayPass(ctx)
	}
	if w.due("release_refresh_last", 7*24*time.Hour) {
		w.setNow("release_refresh_last")
		w.refreshReleaseDates(ctx)
	}
	w.maybeBacklog(ctx)
	w.maybeBackup()
}

// maybeBackup runs the scheduled backup (default weekly, keep 4) unless the
// user turned it off.
func (w *Watcher) maybeBackup() {
	if w.Backups == nil || w.settings.Get("backup_enabled") == "false" || !w.due("backup_last_run", 7*24*time.Hour) {
		return
	}
	w.setNow("backup_last_run")
	name, err := w.Backups.Create()
	if err != nil {
		log.Printf("watcher: scheduled backup: %v", err)
		return
	}
	keep, err := strconv.Atoi(strings.TrimSpace(w.settings.Get("backup_keep")))
	if err != nil || keep < 1 {
		keep = 4
	}
	if err := w.Backups.Prune(keep); err != nil {
		log.Printf("watcher: prune backups: %v", err)
	}
	log.Printf("watcher: scheduled backup written: %s", name)
}

// maybeBacklog runs the weekly backlog pass when it's opted in and due.
func (w *Watcher) maybeBacklog(ctx context.Context) {
	if w.settings.Get("backlog_enabled") != "true" || !w.due("backlog_last_run", 7*24*time.Hour) {
		return
	}
	w.setNow("backlog_last_run")
	w.backlogPass(ctx)
}

// due reports whether the timestamp stored under key is older than interval.
func (w *Watcher) due(key string, interval time.Duration) bool {
	raw := w.settings.Get(key)
	if raw == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return true
	}
	return time.Since(t) >= interval
}

func (w *Watcher) setNow(key string) {
	if err := w.settings.Set(key, time.Now().UTC().Format(time.RFC3339)); err != nil {
		log.Printf("watcher: save %s: %v", key, err)
	}
}

// ---- list polling ----

// pollDueLists polls every enabled list whose last check is older than the
// poll interval plus a small per-list jitter, so lists stagger instead of
// hitting providers in lockstep.
func (w *Watcher) pollDueLists(ctx context.Context) {
	lists, err := w.Lists()
	if err != nil {
		log.Printf("watcher: load lists: %v", err)
		return
	}
	interval := w.pollInterval()
	for _, l := range lists {
		if !l.Enabled || !w.listDue(l, interval) {
			continue
		}
		if _, err := w.PollList(ctx, l.ID); err != nil {
			log.Printf("watcher: poll %q: %v", l.Name, err)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (w *Watcher) listDue(l WatchedList, interval time.Duration) bool {
	if l.LastChecked == "" {
		return true
	}
	last, err := time.Parse("2006-01-02 15:04:05", l.LastChecked)
	if err != nil {
		return true
	}
	jitter := time.Duration(l.ID*7919%int64(interval.Seconds()/4+1)) * time.Second
	return time.Since(last) >= interval+jitter
}

// PollList polls one list now and returns how many new books it routed.
// Poll errors are also recorded on the list row for the UI.
func (w *Watcher) PollList(ctx context.Context, listID int64) (added int, err error) {
	l, err := w.GetList(listID)
	if err != nil {
		return 0, err
	}
	var etag string
	_ = w.db.QueryRow(`SELECT COALESCE(etag, '') FROM watched_lists WHERE id = ?`, listID).Scan(&etag)

	var entries []Entry
	var newEtag string
	switch l.Kind {
	case "goodreads_rss":
		userID, shelf, refErr := SplitGoodreadsRef(l.SourceRef)
		if refErr != nil {
			w.markChecked(listID, etag, refErr.Error())
			return 0, refErr
		}
		var notModified bool
		entries, newEtag, notModified, err = w.goodreads.Fetch(ctx, userID, shelf, etag)
		if err != nil {
			w.markChecked(listID, etag, err.Error())
			return 0, err
		}
		if notModified {
			w.markChecked(listID, etag, "")
			return 0, nil
		}
	case "hardcover":
		metas, hcErr := w.hardcover.ListBooks(ctx, l.SourceRef)
		if hcErr != nil {
			w.markChecked(listID, etag, hcErr.Error())
			return 0, hcErr
		}
		keep := metas[:0:0]
		for _, m := range metas {
			if m.HardcoverID != "" {
				keep = append(keep, m)
			}
		}
		return w.applyEntries(ctx, l, metasToEntries(keep), keep, "")
	default:
		return 0, fmt.Errorf("unknown list kind %q", l.Kind)
	}
	return w.applyEntries(ctx, l, entries, nil, newEtag)
}

// metasToEntries derives diff entries for a hardcover list, index-aligned
// with its (pre-filtered) metas.
func metasToEntries(metas []metadata.BookMeta) []Entry {
	out := make([]Entry, len(metas))
	for i, m := range metas {
		author := ""
		if len(m.Authors) > 0 {
			author = m.Authors[0]
		}
		out[i] = Entry{Title: m.Title, Author: author, ISBN13: m.ISBN13}
	}
	return out
}

// applyEntries diffs the feed against what the list has already routed:
// new entries are added (and searched, when configured), vanished entries get
// the list's on-remove behavior. Metas, when given (hardcover), carry richer
// metadata for the same index positions as entries.
func (w *Watcher) applyEntries(ctx context.Context, l *WatchedList, entries []Entry, metas []metadata.BookMeta, etag string) (int, error) {
	known, err := w.knownItems(l.ID)
	if err != nil {
		w.markChecked(l.ID, etag, err.Error())
		return 0, err
	}

	current := map[string]bool{}
	added := 0
	var firstErr error
	for i, e := range entries {
		ext := externalID(l.Kind, e, metas, i)
		if ext == "" {
			continue
		}
		current[ext] = true
		if _, ok := known[ext]; ok {
			_ = w.touchItem(l.ID, ext)
			continue
		}
		var meta metadata.BookMeta
		if metas != nil && i < len(metas) {
			meta = metas[i]
		} else {
			// "goodreads_rss", not "goodreads": the feed is a thin reference
			// (no description/publisher/dates), so enrich must still query the
			// Goodreads detail page — which the by-id lookup does from the
			// GoodreadsID here — rather than treat it as a complete record.
			meta = metadata.BookMeta{
				Provider: "goodreads_rss", Title: e.Title, GoodreadsID: e.GoodreadsID, ISBN13: e.ISBN13,
				CoverURL: e.CoverURL,
			}
			if e.Author != "" {
				meta.Authors = []string{e.Author}
			}
		}
		bookID, err := w.routeBook(ctx, l, meta)
		if err != nil {
			log.Printf("watcher: %s: route %q: %v", l.Name, e.Title, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := w.rememberItem(l.ID, ext, bookID, e.Title); err != nil {
			log.Printf("watcher: remember item: %v", err)
		}
		if bookID != 0 {
			added++
		}
	}

	// entries that left the feed get the on-remove behavior — default nothing;
	// Goodreads moving finished books off want-to-read must never delete files
	for ext, bookID := range known {
		if current[ext] {
			continue
		}
		w.handleRemoval(l, ext, bookID)
	}

	errText := ""
	if firstErr != nil {
		errText = firstErr.Error()
	}
	w.markChecked(l.ID, etag, errText)
	return added, firstErr
}

func externalID(kind string, e Entry, metas []metadata.BookMeta, i int) string {
	if kind == "hardcover" {
		if metas != nil && i < len(metas) && metas[i].HardcoverID != "" {
			return "hc:" + metas[i].HardcoverID
		}
		return ""
	}
	if e.GoodreadsID == "" {
		return ""
	}
	return "gr:" + e.GoodreadsID
}

// routeBook lands one list entry in the list's library: enrich → upsert →
// attach → monitor scope → cover → optional search. Returns 0 (no error)
// when the book is filtered (box set / user exclusion) or user-blocked.
func (w *Watcher) routeBook(ctx context.Context, l *WatchedList, meta metadata.BookMeta) (int64, error) {
	// feeds bypass the metadata chain, so the exclusion filter applies here
	if metadata.Excluded(meta.Title, w.settings.ExcludePatterns()) {
		log.Printf("watcher: %s: %q filtered (box set / excluded pattern)", l.Name, meta.Title)
		return 0, nil
	}
	meta = w.adoptHardcover(ctx, meta)
	meta = w.chain.Enrich(ctx, meta)
	bookID, err := w.catalog.UpsertBook(meta)
	if err != nil {
		return 0, err
	}
	if w.userBlocked(bookID) {
		return 0, nil
	}
	// A book the library already holds is not an add: no history entry and —
	// crucially — no search. A shelved copy needs nothing (a "read" list full
	// of owned books must not re-download the shelf), and a still-wanted one
	// was searched when it first arrived; re-searches are the backlog's job.
	// Monitor-scope cascades below still apply — a new author/series-scoped
	// list legitimately expands around a book you already have.
	attached, onShelf, err := w.catalog.LibraryState(bookID, l.LibraryID)
	if err != nil {
		return 0, err
	}
	if !attached {
		if err := w.catalog.AddToLibrary(bookID, l.LibraryID, true); err != nil {
			return 0, err
		}
	}
	if err := w.covers.Ensure(ctx, bookID, meta.CoverURL); err != nil {
		log.Printf("watcher: cover for %q: %v", meta.Title, err) // retries on refresh
	}

	book, err := w.catalog.GetBook(bookID)
	if err != nil {
		return 0, err
	}
	switch l.MonitorScope {
	case "series":
		if book.SeriesID != nil {
			if err := w.catalog.MarkSeriesTracked(*book.SeriesID); err != nil {
				log.Printf("watcher: monitor series: %v", err)
			}
		}
	case "author":
		if err := w.catalog.SetAuthorMonitored(book.AuthorID, true); err != nil {
			log.Printf("watcher: monitor author: %v", err)
		}
		// author scope = the backlist too: pull the bibliography into the
		// list's library, monitored, so it all becomes wanted
		if n, err := w.ExpandAuthor(ctx, book.AuthorID, Attach{LibraryID: l.LibraryID, Monitored: true}); err != nil {
			log.Printf("watcher: expand backlist for %q: %v", book.Author, err)
		} else if n > 0 {
			log.Printf("watcher: %s: %d backlist book(s) for %q", l.Name, n, book.Author)
		}
	}
	if attached {
		log.Printf("watcher: %s: %q already in library (%s) — not re-added or searched",
			l.Name, meta.Title, map[bool]string{true: "on shelf", false: "wanted"}[onShelf])
		return bookID, nil
	}
	_ = w.catalog.AddHistory(bookID, l.LibraryID, "list_add", fmt.Sprintf("%q added from list %s", meta.Title, l.Name))
	log.Printf("watcher: %s: added %q", l.Name, meta.Title)

	if l.SearchOnAdd {
		grabbed, err := w.engine.AutoGrab(ctx, bookID, l.LibraryID, l.QualityProfileID)
		w.recordAttempt(bookID)
		if err != nil {
			log.Printf("watcher: search on add %q: %v", meta.Title, err)
		} else if grabbed {
			w.engine.DeliverPending()
		}
	}
	return bookID, nil
}

// adoptHardcover resolves a Goodreads-seeded list entry against Hardcover
// and, on a confident match, adopts Hardcover as CANONICAL: its clean title,
// cover, series, description and genres replace the noisy feed values instead
// of gap-filling around them. Goodreads keeps identity — the GR id rides
// along, and Enrich still gap-fills ratings and anything Hardcover lacks from
// the Goodreads detail page. Anything short of a confident match (score ≥ 60
// with author overlap) returns the seed unchanged: adopting the wrong book is
// worse than keeping a messy title.
func (w *Watcher) adoptHardcover(ctx context.Context, seed metadata.BookMeta) metadata.BookMeta {
	if w.hardcover == nil || !w.hardcover.WorksConfigured() || seed.HardcoverID != "" || seed.Title == "" {
		return seed
	}
	author := ""
	if len(seed.Authors) > 0 {
		author = seed.Authors[0]
	}
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var candidates []metadata.BookMeta
	if seed.ISBN13 != "" {
		candidates, _ = w.hardcover.Search(hctx, metadata.SearchParams{ISBN: seed.ISBN13, Limit: 3})
	}
	if len(candidates) == 0 {
		var err error
		candidates, err = w.hardcover.Search(hctx, metadata.SearchParams{Title: seed.Title, Author: author, Limit: 5})
		if err != nil {
			log.Printf("watcher: hardcover match %q: %v", seed.Title, err)
			return seed
		}
	}
	best, ok := metadata.BestConfidentMatch(candidates, seed.Title, author)
	if !ok {
		return seed
	}
	if best.Title != seed.Title {
		log.Printf("watcher: adopting hardcover %s: %q → %q", best.HardcoverID, seed.Title, best.Title)
	}
	// best is Hardcover's full view (Provider "hardcover", so Enrich won't
	// re-query it); the seed fills whatever Hardcover lacks — GR id, ISBN,
	// and the feed cover only when Hardcover has none.
	metadata.Merge(&best, seed)
	return best
}

// userBlocked reports whether the user removed this book with "delete and
// block", which watched lists must respect by never re-adding it.
func (w *Watcher) userBlocked(bookID int64) bool {
	var n int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM blocklist WHERE book_id = ? AND source = 'user'`, bookID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

func (w *Watcher) handleRemoval(l *WatchedList, ext string, bookID int64) {
	switch l.OnRemove {
	case "unmonitor":
		if bookID != 0 {
			if err := w.catalog.SetMonitored(l.LibraryID, bookID, false); err != nil {
				log.Printf("watcher: unmonitor on remove: %v", err)
			}
			_ = w.catalog.AddHistory(bookID, l.LibraryID, "list_remove", "left list "+l.Name+" — unmonitored")
		}
	case "delete":
		if bookID != 0 {
			// removes the membership and this library's file; other libraries'
			// hardlinks keep the bytes
			if err := w.catalog.RemoveBookFromLibrary(bookID, l.LibraryID, "file"); err != nil {
				log.Printf("watcher: delete on remove: %v", err)
				return
			}
			_ = w.catalog.AddHistory(bookID, l.LibraryID, "list_remove", "left list "+l.Name+" — removed from library")
		}
	default: // "nothing"
		return
	}
	if err := w.forgetItem(l.ID, ext); err != nil {
		log.Printf("watcher: forget item: %v", err)
	}
}

// ---- release-day calendar triggers ----

// taperDays are the offsets (days since release) that trigger a search:
// release day, a short taper after, then the book falls to the backlog pass.
var taperDays = map[int]bool{0: true, 1: true, 2: true, 3: true, 7: true, 14: true}

// releaseDayPass searches for monitored, missing books whose release date
// offset is on the taper — at most once per book per day.
func (w *Watcher) releaseDayPass(ctx context.Context) {
	rows, err := w.db.Query(`
		SELECT b.id, lb.library_id, b.release_date
		FROM books b JOIN library_books lb ON lb.book_id = b.id
		WHERE lb.monitored = 1 AND (lb.file_path IS NULL OR lb.file_path = '')
		  AND b.release_date IS NOT NULL AND length(b.release_date) = 10`)
	if err != nil {
		log.Printf("watcher: release-day query: %v", err)
		return
	}
	type hit struct {
		bookID, libraryID int64
	}
	var hits []hit
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for rows.Next() {
		var bookID, libraryID int64
		var date string
		if err := rows.Scan(&bookID, &libraryID, &date); err != nil {
			continue
		}
		rel, err := time.Parse("2006-01-02", date)
		if err != nil {
			continue
		}
		offset := int(today.Sub(rel).Hours() / 24)
		if taperDays[offset] {
			hits = append(hits, hit{bookID, libraryID})
		}
	}
	rows.Close()

	for _, h := range hits {
		if w.attemptedToday(h.bookID) {
			continue
		}
		log.Printf("watcher: release-day search for book %d", h.bookID)
		if _, err := w.engine.AutoGrab(ctx, h.bookID, h.libraryID, 0); err != nil {
			log.Printf("watcher: release-day grab: %v", err)
		}
		w.recordAttempt(h.bookID)
		w.engine.DeliverPending()
		if !w.pause(ctx, w.Pace) {
			return
		}
	}
}

// ---- weekly release-date refresh ----

// refreshReleaseDates re-fetches metadata for monitored unreleased books —
// release dates slip, and the calendar should slip with them.
func (w *Watcher) refreshReleaseDates(ctx context.Context) {
	books, err := w.catalog.ListBooks(0, 0, 0)
	if err != nil {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	refreshed := 0
	for _, b := range books {
		if !b.Monitored || b.FilePath != "" {
			continue
		}
		// unreleased = future date, or a bare year that isn't over
		unreleased := (len(b.ReleaseDate) == 10 && b.ReleaseDate > today) ||
			(len(b.ReleaseDate) == 4 && b.ReleaseDate >= today[:4])
		if !unreleased {
			continue
		}
		if refreshed >= 30 { // weekly budget; the rest catch up next week
			return
		}
		refreshed++
		skeleton := metadata.BookMeta{
			Provider: "refresh", Title: b.Title,
			GoodreadsID: b.GoodreadsID, HardcoverID: b.HardcoverID, ISBN13: b.ISBN13,
		}
		if b.Author != "" {
			skeleton.Authors = []string{b.Author}
		}
		meta := w.chain.Enrich(ctx, skeleton)
		if meta.ReleaseDate != "" && meta.ReleaseDate != b.ReleaseDate {
			log.Printf("watcher: %q release date %s → %s", b.Title, b.ReleaseDate, meta.ReleaseDate)
		}
		if _, err := w.catalog.UpsertBook(meta); err != nil {
			log.Printf("watcher: refresh %q: %v", b.Title, err)
		}
		if !w.pause(ctx, w.Pace) {
			return
		}
	}
}

// ---- backlog pass ----

// backlogPass re-searches missing and cutoff-unmet books, rate-limited.
// Strictly opt-in (backlog_enabled), weekly, capped per pass.
func (w *Watcher) backlogPass(ctx context.Context) {
	books, err := w.catalog.ListBooks(0, 0, 0)
	if err != nil {
		return
	}
	type target struct {
		bookID, libraryID int64
		title             string
	}
	var targets []target
	for _, b := range books {
		if b.Monitored && b.LibraryID != 0 && b.FilePath == "" && released(b.ReleaseDate) {
			targets = append(targets, target{b.ID, b.LibraryID, b.Title})
		}
	}
	unmet, libIDs, err := w.engine.CutoffUnmet()
	if err == nil {
		for i, b := range unmet {
			targets = append(targets, target{b.ID, libIDs[i], b.Title})
		}
	}

	log.Printf("watcher: backlog pass over %d books", len(targets))
	searched := 0
	for _, t := range targets {
		if searched >= 50 { // weekly budget
			log.Printf("watcher: backlog budget reached; %d books wait for next week", len(targets)-searched)
			return
		}
		if w.attemptedToday(t.bookID) {
			continue
		}
		searched++
		if _, err := w.engine.AutoGrab(ctx, t.bookID, t.libraryID, 0); err != nil {
			log.Printf("watcher: backlog %q: %v", t.title, err)
		}
		w.recordAttempt(t.bookID)
		w.engine.DeliverPending()
		if !w.pause(ctx, w.Pace) {
			return
		}
	}
}

// released reports whether a release date is empty (assume out) or in the past.
func released(date string) bool {
	if date == "" {
		return true
	}
	today := time.Now().UTC().Format("2006-01-02")
	if len(date) == 4 {
		return date <= today[:4]
	}
	return date <= today
}

// ---- search attempt bookkeeping ----

func (w *Watcher) recordAttempt(bookID int64) {
	if _, err := w.db.Exec(`INSERT INTO search_attempts (book_id, last_searched) VALUES (?, datetime('now'))
		ON CONFLICT(book_id) DO UPDATE SET last_searched = datetime('now')`, bookID); err != nil {
		log.Printf("watcher: record attempt: %v", err)
	}
}

func (w *Watcher) attemptedToday(bookID int64) bool {
	var n int
	err := w.db.QueryRow(`SELECT COUNT(*) FROM search_attempts
		WHERE book_id = ? AND last_searched >= date('now')`, bookID).Scan(&n)
	return err == nil && n > 0
}

// pause waits d unless ctx ends first; reports whether to continue.
func (w *Watcher) pause(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
