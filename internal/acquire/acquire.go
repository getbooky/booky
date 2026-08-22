// Package acquire is the acquisition engine: it searches every
// configured source for a wanted book, ranks what comes back, grabs the best
// candidate, and tracks it through the queue until the importer takes over.
package acquire

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/directdl"
	"github.com/getbooky/booky/internal/importer"
	"github.com/getbooky/booky/internal/prowlarr"
	"github.com/getbooky/booky/internal/release"
	"github.com/getbooky/booky/internal/sabnzbd"
	"github.com/getbooky/booky/internal/settings"
	"github.com/getbooky/booky/internal/zlibrary"
)

type Engine struct {
	db       *sql.DB
	catalog  *catalog.Store
	settings *settings.Store
	importer *importer.Importer
	covers   *catalog.CoverCache
	annas    *directdl.Client

	// running holds a cancel func per queue row whose direct download is in
	// flight in this process — SAB jobs are cancelled at SAB instead.
	mu      sync.Mutex
	running map[int64]context.CancelFunc
}

// The stock mirror lists live in the settings defaults (internal/settings),
// where the UI can show them as removable pills.

func New(db *sql.DB, cat *catalog.Store, cfg *settings.Store, imp *importer.Importer, covers *catalog.CoverCache) *Engine {
	e := &Engine{db: db, catalog: cat, settings: cfg, importer: imp, covers: covers}
	e.annas = directdl.New(
		directdl.Mirrors{Annas: e.mirrorList("annas_mirrors")},
		func() string { return e.settings.Get("annas_key") },
	)
	return e
}

// Zlib is rebuilt per call so settings edits apply without a restart.
func (e *Engine) Zlib() *zlibrary.Client {
	return zlibrary.New(
		e.mirrorList("zlib_domains"),
		e.settings.Get("zlib_email"), e.settings.Get("zlib_password"),
	)
}

// Annas exposes the Anna's client for connection tests.
func (e *Engine) Annas() *directdl.Client { return e.annas }

// mirrorList reads a newline-separated mirror setting. The stock entries
// live in the settings defaults (removable in the UI); an emptied list
// means the source has no mirrors and drops out of searches.
func (e *Engine) mirrorList(key string) func() []string {
	return func() []string {
		return directdl.SplitMirrors(e.settings.Get(key))
	}
}

// Prowlarr and SAB clients are rebuilt per call so settings edits apply
// without a restart.
func (e *Engine) Prowlarr() *prowlarr.Client {
	return prowlarr.New(e.settings.Get("prowlarr_url"), e.settings.Get("prowlarr_api_key"))
}

func (e *Engine) Sab() *sabnzbd.Client {
	return sabnzbd.New(e.settings.Get("sab_url"), e.settings.Get("sab_api_key"), e.settings.Get("sab_category"))
}

func (e *Engine) sourceOrder() []string {
	v := e.settings.Get("source_order")
	if strings.TrimSpace(v) == "" {
		v = "prowlarr,annas,zlibrary"
	}
	parts := strings.Split(v, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// profileFor loads the ranking slice of a quality profile: the override
// profile when profileID is non-zero (watched lists can carry one), otherwise
// the library's own.
func (e *Engine) profileFor(libraryID, profileID int64) (release.Profile, error) {
	var formatsJSON, languages, preferred, avoided string
	var err error
	if profileID != 0 {
		err = e.db.QueryRow(`SELECT formats, language, preferred_terms, avoided_terms
			FROM quality_profiles WHERE id = ?`, profileID).Scan(&formatsJSON, &languages, &preferred, &avoided)
	} else {
		err = e.db.QueryRow(`
			SELECT p.formats, p.language, p.preferred_terms, p.avoided_terms
			FROM libraries l JOIN quality_profiles p ON p.id = l.quality_profile_id
			WHERE l.id = ?`, libraryID).Scan(&formatsJSON, &languages, &preferred, &avoided)
	}
	if err != nil {
		return release.Profile{}, fmt.Errorf("load profile: %w", err)
	}
	var formats []string
	if err := json.Unmarshal([]byte(formatsJSON), &formats); err != nil {
		formats = []string{"epub"}
	}
	split := func(s string) []string {
		var out []string
		for _, t := range strings.Split(s, ",") {
			if t = strings.TrimSpace(t); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	splitLangs := func(s string) []string {
		var out []string
		for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' }) {
			if t = strings.TrimSpace(t); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	return release.Profile{
		Formats: formats, PreferredTerms: split(preferred), AvoidedTerms: split(avoided),
		Languages: splitLangs(languages),
	}, nil
}

// Search queries every configured source for the book and returns ranked,
// blocklist-filtered candidates. Sources that fail are skipped, not fatal —
// one dead mirror must never hide results from the others.
func (e *Engine) Search(ctx context.Context, bookID, libraryID int64) ([]release.Release, error) {
	return e.SearchWithProfile(ctx, bookID, libraryID, 0)
}

// SourceStat reports how one provider fared in a search, so the UI can show
// which sources were tried and why one returned nothing (disabled, not
// configured, errored, or simply no hits) instead of silently omitting it.
type SourceStat struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	Disabled   bool   `json:"disabled,omitempty"`
	Count      int    `json:"count"`
	Error      string `json:"error,omitempty"`
}

// sourceEnabled reads a per-source kill switch; anything but an explicit
// "false" counts as on, so old installs keep their behavior.
func (e *Engine) sourceEnabled(key string) bool {
	return e.settings.Get(key) != "false"
}

// SearchWithProfile is Search with an optional quality-profile override
// (0 = use the library's profile) — watched lists route through here.
func (e *Engine) SearchWithProfile(ctx context.Context, bookID, libraryID, profileID int64) ([]release.Release, error) {
	rels, _, err := e.SearchDetailed(ctx, bookID, libraryID, profileID)
	return rels, err
}

// SearchDetailed is SearchWithProfile plus per-source stats.
func (e *Engine) SearchDetailed(ctx context.Context, bookID, libraryID, profileID int64) ([]release.Release, []SourceStat, error) {
	book, err := e.catalog.GetBook(bookID)
	if err != nil {
		return nil, nil, err
	}
	query := strings.TrimSpace(book.Title + " " + book.Author)

	var all []release.Release
	var stats []SourceStat
	run := func(name string, configured, enabled bool, search func() ([]release.Release, error)) {
		st := SourceStat{Name: name, Configured: configured, Disabled: !enabled}
		if configured && enabled {
			rels, err := search()
			if err != nil {
				st.Error = err.Error()
				log.Printf("acquire: %s search: %v", name, err)
			} else {
				st.Count = len(rels)
				all = append(all, rels...)
			}
		}
		stats = append(stats, st)
	}

	p := e.Prowlarr()
	run("prowlarr", p.Configured(), e.sourceEnabled("prowlarr_enabled"), func() ([]release.Release, error) { return p.Search(ctx, query) })
	run("annas", e.annas.Configured(), e.sourceEnabled("annas_enabled"), func() ([]release.Release, error) { return e.annas.Search(ctx, query) })
	z := e.Zlib()
	run("zlibrary", z.Configured(), e.sourceEnabled("zlib_enabled"), func() ([]release.Release, error) { return z.Search(ctx, query) })

	// torrents are deliberately unsupported — only usenet and direct results
	// are shown or grabbed
	noTorrents := all[:0:0]
	for _, r := range all {
		if r.Protocol != "torrent" {
			noTorrents = append(noTorrents, r)
		}
	}
	all = noTorrents

	all, err = e.dropBlocked(bookID, all)
	if err != nil {
		return nil, stats, err
	}
	profile, err := e.profileFor(libraryID, profileID)
	if err != nil {
		return nil, stats, err
	}
	profile.BookTitle = book.Title
	return release.Rank(all, profile, e.sourceOrder()), stats, nil
}

func (e *Engine) dropBlocked(bookID int64, rels []release.Release) ([]release.Release, error) {
	rows, err := e.db.Query(`SELECT release_name FROM blocklist WHERE book_id = ? OR book_id IS NULL`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blocked := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		blocked[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keep := rels[:0:0]
	for _, r := range rels {
		if !blocked[strings.ToLower(r.Title)] {
			keep = append(keep, r)
		}
	}
	return keep, nil
}

// Block adds a release to the blocklist so it's never grabbed again for this
// book, and records why.
func (e *Engine) Block(bookID int64, rel release.Release, reason string) error {
	_, err := e.db.Exec(`INSERT INTO blocklist (book_id, release_name, source, reason) VALUES (?, ?, ?, ?)`,
		bookID, rel.Title, rel.Source, reason)
	return err
}

// Grab downloads a chosen release. Usenet goes to SABnzbd under Booky's
// category; direct sources resolve and download immediately. Either way a
// queue row tracks it. Returns the queue id.
func (e *Engine) Grab(ctx context.Context, bookID, libraryID int64, rel release.Release) (int64, error) {
	res, err := e.db.Exec(`INSERT INTO queue (book_id, library_id, release_title, source, protocol, status)
		VALUES (?, ?, ?, ?, ?, 'queued')`, bookID, libraryID, rel.Title, rel.Source, rel.Protocol)
	if err != nil {
		return 0, err
	}
	queueID, _ := res.LastInsertId()

	fail := func(cause error) (int64, error) {
		e.setQueue(queueID, "failed", cause.Error())
		// blocklisting is a verdict on the RELEASE — a gated free partner or a
		// cancelled request says nothing about it, and blocklisting those
		// poisons every candidate tried during a gated window ("keyless
		// downloads always fail" forever, even after the wall lifts)
		transient := errors.Is(cause, directdl.ErrPartnersGated) ||
			errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)
		if !transient {
			if err := e.Block(bookID, rel, cause.Error()); err != nil {
				log.Printf("acquire: blocklist: %v", err)
			}
		}
		return queueID, cause
	}

	switch rel.Protocol {
	case "usenet":
		sab := e.Sab()
		if !sab.Configured() {
			return fail(fmt.Errorf("SABnzbd is not configured"))
		}
		nzoID, err := sab.AddURL(ctx, rel.DownloadURL, rel.Title)
		if err != nil {
			return fail(err)
		}
		e.setQueueExternal(queueID, "downloading", "sent to SABnzbd", nzoID)
		_ = e.catalog.AddHistory(bookID, libraryID, "grabbed", grabDetail(rel, "SABnzbd"))
		return queueID, nil

	case "direct":
		e.setQueue(queueID, "downloading", "fetching from "+rel.Source)
		// registered so Cancel can cut this fetch mid-flight; the deferred
		// unregister also runs before fail(), so a cancelled row that was
		// already deleted is never re-marked
		ctx, cancelRun := context.WithCancel(ctx)
		defer cancelRun()
		e.mu.Lock()
		if e.running == nil {
			e.running = map[int64]context.CancelFunc{}
		}
		e.running[queueID] = cancelRun
		e.mu.Unlock()
		defer func() {
			e.mu.Lock()
			delete(e.running, queueID)
			e.mu.Unlock()
		}()
		dir := e.settings.Get("downloads_dir")
		if dir == "" {
			dir = "/data/downloads/booky"
		}
		var path string
		var err error
		switch {
		case strings.HasPrefix(rel.DownloadURL, "zlib:"):
			path, err = e.Zlib().Download(ctx, rel.DownloadURL, dir, rel.Format)
		case strings.HasPrefix(rel.DownloadURL, "md5:"):
			path, err = e.annas.Download(ctx, rel.DownloadURL, dir, rel.Format)
		default:
			err = fmt.Errorf("unknown direct source for %q", rel.DownloadURL)
		}
		if err != nil {
			return fail(fmt.Errorf("download: %w", err))
		}
		e.setQueueExternal(queueID, "importing", "downloaded, waiting for import", path)
		_ = e.catalog.AddHistory(bookID, libraryID, "grabbed", grabDetail(rel, path))
		return queueID, nil

	default:
		// torrents ride through Prowlarr's download URL too, but a torrent
		// client integration is not built yet — refuse loudly instead of
		// pretending.
		return fail(fmt.Errorf("no download client for protocol %q", rel.Protocol))
	}
}

// grabDetail writes the history line for a grab. The source belongs in it:
// "which indexer did this come from" is the first question asked of a book
// that arrived wrong, and the queue row that used to answer it is gone by
// the time anyone looks.
func grabDetail(rel release.Release, dest string) string {
	src := rel.Source
	if src == "" {
		src = "unknown source"
	}
	return fmt.Sprintf("%s from %s → %s", rel.Title, src, dest)
}

func (e *Engine) setQueue(id int64, status, detail string) {
	if _, err := e.db.Exec(`UPDATE queue SET status = ?, detail = ?, updated_at = datetime('now') WHERE id = ?`,
		status, detail, id); err != nil {
		log.Printf("acquire: update queue %d: %v", id, err)
	}
}

func (e *Engine) setQueueExternal(id int64, status, detail, externalID string) {
	if _, err := e.db.Exec(`UPDATE queue SET status = ?, detail = ?, external_id = ?, updated_at = datetime('now') WHERE id = ?`,
		status, detail, externalID, id); err != nil {
		log.Printf("acquire: update queue %d: %v", id, err)
	}
}

// QueueItem is a queue row joined with its book for display.
type QueueItem struct {
	ID           int64  `json:"id"`
	BookID       int64  `json:"bookId"`
	LibraryID    int64  `json:"libraryId"`
	BookTitle    string `json:"bookTitle"`
	ReleaseTitle string `json:"releaseTitle"`
	Source       string `json:"source"`
	Protocol     string `json:"protocol"`
	Status       string `json:"status"`
	ExternalID   string `json:"externalId,omitempty"`
	Detail       string `json:"detail,omitempty"`
	// Both are RFC3339 (see catalog.SQLTime): CreatedAt is when the grab was
	// sent, UpdatedAt when the row last changed state — the two times the
	// queue answers "how long has this been sitting here?" with.
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// Queue returns recent download activity, narrowed to the account's libraries
// when a Visibility is given. The narrowing is in the query for the same
// reason as history: filtering a global "last 200" afterwards would show a
// scoped user only the leftovers of someone else's busy day.
func (e *Engine) Queue(v *catalog.Visibility) ([]QueueItem, error) {
	ph, libArgs := catalog.IDPlaceholders(v)
	args := []any{catalog.UnscopedFlag(v)}
	args = append(args, libArgs...)
	rows, err := e.db.Query(fmt.Sprintf( //nolint:gosec // G201: placeholders only, see catalog.IDPlaceholders
		`SELECT q.id, q.book_id, q.library_id, b.title, q.release_title, q.source,
		        q.protocol, q.status, q.external_id, q.detail, q.created_at, q.updated_at
		 FROM queue q JOIN books b ON b.id = q.book_id
		 WHERE (? = 1 OR q.library_id IN (%s))
		 ORDER BY q.id DESC LIMIT 200`, ph), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		var it QueueItem
		if err := rows.Scan(&it.ID, &it.BookID, &it.LibraryID, &it.BookTitle, &it.ReleaseTitle,
			&it.Source, &it.Protocol, &it.Status, &it.ExternalID, &it.Detail, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.CreatedAt, it.UpdatedAt = db.SQLTime(it.CreatedAt), db.SQLTime(it.UpdatedAt)
		out = append(out, it)
	}
	return out, rows.Err()
}

// PendingImports returns direct downloads that finished and wait for import.
func (e *Engine) PendingImports() ([]QueueItem, error) {
	items, err := e.Queue(nil)
	if err != nil {
		return nil, err
	}
	keep := items[:0:0]
	for _, it := range items {
		if it.Status == "importing" && it.ExternalID != "" {
			keep = append(keep, it)
		}
	}
	return keep, nil
}

// Cancel aborts a queue row and cleans up after it: a SAB job is deleted at
// SAB with its files, an in-flight direct fetch is cut, a downloaded file
// waiting for import is removed from disk, and the row disappears with a
// history entry. Deliberately NO blocklist and NO cascade — cancelling is a
// choice about this download, not a verdict on the release, so nothing else
// gets grabbed in its place and the release stays available for a re-grab.
func (e *Engine) Cancel(ctx context.Context, id int64) error {
	var it QueueItem
	err := e.db.QueryRow(`SELECT q.book_id, q.library_id, q.release_title, q.protocol,
			q.status, COALESCE(q.external_id, '')
		FROM queue q WHERE q.id = ?`, id).
		Scan(&it.BookID, &it.LibraryID, &it.ReleaseTitle, &it.Protocol, &it.Status, &it.ExternalID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("queue item not found")
	}
	if err != nil {
		return err
	}

	switch {
	case it.Protocol == "usenet" && it.ExternalID != "" &&
		(it.Status == "downloading" || it.Status == "import_failed"):
		// the job (and any partial or finished files) lives at SAB; if SAB
		// can't be reached the row stays, so the cancel can be retried
		// rather than orphaning a live job
		sab := e.Sab()
		if !sab.Configured() {
			return fmt.Errorf("SABnzbd is not configured — can't remove the job")
		}
		// a still-running job is in SAB's queue; one that just finished has
		// already moved to its history — try both homes before giving up
		if err := sab.DeleteQueue(ctx, it.ExternalID, true); err != nil {
			if err2 := sab.Delete(ctx, it.ExternalID, true); err2 != nil {
				return fmt.Errorf("removing the SABnzbd job: %w", err)
			}
		}
	case it.Protocol == "direct" && it.Status == "downloading":
		// in flight in this process: cutting the context makes the fetch
		// return context.Canceled, which Grab treats as transient — no
		// blocklist — and its queue update lands on a row already gone
		e.mu.Lock()
		if cancelRun, ok := e.running[id]; ok {
			cancelRun()
		}
		e.mu.Unlock()
	case it.Protocol == "direct" && it.ExternalID != "" &&
		(it.Status == "importing" || it.Status == "import_failed"):
		// the downloaded file sits on disk waiting for an import that will
		// now never happen
		if err := os.Remove(it.ExternalID); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", it.ExternalID, err)
		}
	}
	// queued and failed rows have nothing external to clean up

	if _, err := e.db.Exec(`DELETE FROM queue WHERE id = ?`, id); err != nil {
		return err
	}
	_ = e.catalog.AddHistory(it.BookID, it.LibraryID, "cancelled", it.ReleaseTitle)
	return nil
}

// RetryImport re-attempts delivery of a failed queue row after the user
// fixed whatever broke (permissions, disk). The file is already on disk —
// direct rows re-enter the importing state and deliver immediately; usenet
// rows re-enter downloading so the next SAB poll re-imports from the
// finished folder (failed/unimportable jobs are deliberately kept in SAB).
func (e *Engine) RetryImport(ctx context.Context, id int64) error {
	var status, protocol, externalID string
	err := e.db.QueryRow(`SELECT status, protocol, COALESCE(external_id, '') FROM queue WHERE id = ?`, id).
		Scan(&status, &protocol, &externalID)
	if err != nil {
		return fmt.Errorf("queue row %d: %w", id, err)
	}
	if status != "import_failed" {
		return fmt.Errorf("only failed imports can be retried (row is %q) — a failed download has nothing on disk", status)
	}
	if externalID == "" {
		return fmt.Errorf("nothing to retry — no downloaded file recorded; search again instead")
	}
	switch protocol {
	case "direct":
		if _, err := os.Stat(externalID); err != nil {
			return fmt.Errorf("downloaded file is gone (%s) — search again instead", externalID)
		}
		e.setQueue(id, "importing", "retrying import")
		e.DeliverPending()
	case "usenet":
		e.setQueue(id, "downloading", "retrying import from SABnzbd")
		e.PollSab(ctx)
	default:
		return fmt.Errorf("no retry path for protocol %q", protocol)
	}
	return nil
}

// MarkDone clears a queue row after a successful import. The queue is a work
// list, not a ledger: once the bytes are on the shelf the row has nothing
// left to say, and leaving it there buries the two or three rows that still
// need attention under a week of finished ones. The event isn't lost —
// Deliver writes the "imported" history entry, with the delivered path,
// before this is called.
func (e *Engine) MarkDone(id int64) {
	if _, err := e.db.Exec(`DELETE FROM queue WHERE id = ?`, id); err != nil {
		log.Printf("acquire: clear queue row %d: %v", id, err)
	}
}

// MarkFailed flips a queue row after an import failure.
func (e *Engine) MarkFailed(id int64, detail string) { e.setQueue(id, "failed", detail) }
