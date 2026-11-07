package acquire

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/prowlarr"
	"github.com/getbooky/booky/internal/release"
)

// Audiobooks are searched for and grabbed beside ebooks, with their own
// profile. Only Prowlarr's audiobook category is searched — the direct
// sources carry ebooks — and only usenet results are taken, as for ebooks.
// A finished download is handed to the audiobook import (Engine.ImportAudio),
// which files it beside the ebook and lines it up for read-along.

// Queue row kinds.
const (
	KindEbook     = "ebook"
	KindAudiobook = "audiobook"
)

// ErrNoAudioImport means the engine has no way to file an audiobook — the
// server didn't wire one in.
var ErrNoAudioImport = errors.New("audiobook import isn't available")

// audioProfile loads the audiobook profile as ranking needs it.
func (e *Engine) audioProfile() (release.Profile, error) {
	p, err := e.catalog.GetAudioProfile()
	if err != nil {
		return release.Profile{}, fmt.Errorf("load audiobook profile: %w", err)
	}
	return release.Profile{
		Audio:          true,
		Formats:        p.Formats,
		PreferredTerms: splitTerms(p.PreferredTerms, ","),
		RejectTerms:    splitTerms(p.AvoidedTerms, ","),
		Languages:      splitTerms(p.Languages, ",\n"),
		MinBytes:       p.MinSizeMB << 20,
		MaxBytes:       p.MaxSizeMB << 20,
	}, nil
}

// splitTerms splits a stored list on any of seps, trimming and dropping
// empty entries.
func splitTerms(s, seps string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(seps, r) }) {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// SearchAudio looks for a book's audiobook and returns ranked candidates
// with how each source fared.
func (e *Engine) SearchAudio(ctx context.Context, bookID int64) ([]release.Release, []SourceStat, error) {
	book, err := e.catalog.GetBook(bookID)
	if err != nil {
		return nil, nil, err
	}
	query := strings.TrimSpace(book.Title + " " + book.Author)
	p := e.Prowlarr()
	st := SourceStat{Name: "prowlarr", Configured: p.Configured(), Disabled: !e.sourceEnabled("prowlarr_enabled")}
	var all []release.Release
	if st.Configured && !st.Disabled {
		rels, err := p.SearchIn(ctx, query, prowlarr.CategoryAudiobooks)
		if err != nil {
			st.Error = err.Error()
			log.Printf("acquire: prowlarr audiobook search: %v", err)
		} else {
			st.Count = len(rels)
			for _, r := range rels {
				if r.Protocol != "torrent" { // as for ebooks: usenet only
					all = append(all, r)
				}
			}
		}
	}
	stats := []SourceStat{st}
	all, err = e.dropBlocked(bookID, all)
	if err != nil {
		return nil, stats, err
	}
	profile, err := e.audioProfile()
	if err != nil {
		return nil, stats, err
	}
	profile.BookTitle = book.Title
	return rankForBook(book, all, profile, e.sourceOrder(), stats), stats, nil
}

// GrabAudio sends an audiobook release to SABnzbd and tracks it in the
// queue. Returns the queue id.
func (e *Engine) GrabAudio(ctx context.Context, bookID, libraryID int64, rel release.Release) (int64, error) {
	return e.grab(ctx, bookID, libraryID, rel, KindAudiobook)
}

// AutoGrabAudio searches for a book's audiobook and grabs the best
// acceptable release, moving down the list when a grab fails. It does
// nothing when the book already has its audiobook or one is on the way.
func (e *Engine) AutoGrabAudio(ctx context.Context, bookID, libraryID int64) (bool, error) {
	if e.inFlight(bookID, libraryID, KindAudiobook) {
		log.Printf("acquire: audiobook auto-grab skipped — one for book %d is already in flight", bookID)
		return true, nil
	}
	var have int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM audiobooks WHERE book_id = ?`, bookID).Scan(&have); err == nil && have > 0 {
		return false, nil
	}
	rels, _, err := e.SearchAudio(ctx, bookID)
	if err != nil {
		return false, err
	}
	for _, rel := range rels {
		if rel.Score <= 0 {
			continue
		}
		if _, err := e.GrabAudio(ctx, bookID, libraryID, rel); err != nil {
			log.Printf("acquire: audiobook auto-grab %q failed, trying next: %v", rel.Title, err)
			continue
		}
		return true, nil
	}
	return false, nil
}

// inFlight reports whether a grab of this kind is already queued or on its
// way for the book.
func (e *Engine) inFlight(bookID, libraryID int64, kind string) bool {
	var n int
	err := e.db.QueryRow(`SELECT COUNT(*) FROM queue
		WHERE book_id = ? AND library_id = ? AND kind = ? AND status IN ('queued', 'downloading', 'importing')`,
		bookID, libraryID, kind).Scan(&n)
	return err == nil && n > 0
}

// importAudio files a finished audiobook download in the background. The
// row moves to "importing" first, so the next poll leaves it alone; the SAB
// job is only cleared once the files have been taken, since joining a
// many-part book reads them for minutes.
func (e *Engine) importAudio(it QueueItem, storage string) {
	if e.ImportAudio == nil {
		e.markImportFailed(it, ErrNoAudioImport)
		return
	}
	if !e.startAudioImport(it.ID) {
		return // already being imported
	}
	e.setQueue(it.ID, "importing", "filing the audiobook")
	go func() {
		defer e.endAudioImport(it.ID)
		ctx := context.Background()
		if err := e.ImportAudio(ctx, it.BookID, storage); err != nil {
			e.markImportFailed(it, err)
			return
		}
		e.MarkDone(it.ID)
		if sab := e.Sab(); sab.Configured() {
			if err := sab.Delete(ctx, it.ExternalID, true); err != nil {
				log.Printf("acquire: cleanup sab job %s: %v", it.ExternalID, err)
			}
		}
	}()
}

func (e *Engine) startAudioImport(queueID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.importing == nil {
		e.importing = map[int64]bool{}
	}
	if e.importing[queueID] {
		return false
	}
	e.importing[queueID] = true
	return true
}

func (e *Engine) endAudioImport(queueID int64) {
	e.mu.Lock()
	delete(e.importing, queueID)
	e.mu.Unlock()
}

// audioImportRunning reports whether this process is filing the row's
// audiobook. A row left "importing" by a restart isn't, and goes again.
func (e *Engine) audioImportRunning(queueID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.importing[queueID]
}

// waitAudioImports blocks until no audiobook import is running — for tests.
func (e *Engine) waitAudioImports(ctx context.Context) error {
	for {
		e.mu.Lock()
		n := len(e.importing)
		e.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
