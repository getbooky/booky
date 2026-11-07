package acquire

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/audiobook"
	"github.com/getbooky/booky/internal/auth"
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
	all = groupSets(all)
	profile, err := e.audioProfile()
	if err != nil {
		return nil, stats, err
	}
	profile.BookTitle = book.Title
	return rankForBook(book, all, profile, e.sourceOrder(), stats), stats, nil
}

// groupSets turns every complete set — one book posted as "Part 1 of N"
// … "Part N of N", all parts found — into one release carrying its parts,
// so it's ranked and grabbed as the whole book. Parts of incomplete sets
// stay as they are: flagged, never grabbed on their own.
func groupSets(rels []release.Release) []release.Release {
	type set struct {
		base  string
		of    int
		parts map[int]release.Release
		first int // index of the first part seen, to keep the list's order
	}
	sets := map[string]*set{}
	var order []string
	inSet := map[int]string{}
	for i, r := range rels {
		base, key, n, of, ok := release.SetPart(r.Title)
		if !ok || r.Protocol == "torrent" {
			continue
		}
		s := sets[key]
		if s == nil {
			s = &set{base: base, of: of, parts: map[int]release.Release{}, first: i}
			sets[key] = s
			order = append(order, key)
		}
		if _, dup := s.parts[n]; !dup {
			s.parts[n] = r
		}
		inSet[i] = key
	}
	complete := map[string]release.Release{}
	for _, key := range order {
		s := sets[key]
		if len(s.parts) != s.of {
			continue
		}
		first := s.parts[1]
		whole := release.Release{
			Title:       fmt.Sprintf("%s (in %d parts)", s.base, s.of),
			Source:      first.Source,
			Protocol:    first.Protocol,
			Indexer:     first.Indexer,
			Language:    first.Language,
			DownloadURL: "set:" + key,
		}
		for n := 1; n <= s.of; n++ {
			p := s.parts[n]
			whole.Parts = append(whole.Parts, p)
			whole.SizeBytes += p.SizeBytes
			if p.Protocol != first.Protocol {
				whole.Protocol = "mixed"
			}
		}
		complete[key] = whole
	}
	var out []release.Release
	added := map[string]bool{}
	for i, r := range rels {
		key, part := inSet[i]
		if part {
			if whole, ok := complete[key]; ok {
				if !added[key] {
					added[key] = true
					out = append(out, whole)
				}
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// GrabAudio sends an audiobook release to SABnzbd and tracks it in the
// queue. Returns the queue id. A whole set grabs every part; the id is the
// first part's.
func (e *Engine) GrabAudio(ctx context.Context, bookID, libraryID int64, rel release.Release) (int64, error) {
	if len(rel.Parts) > 0 {
		return e.grabSet(ctx, bookID, libraryID, rel)
	}
	return e.grab(ctx, bookID, libraryID, rel, KindAudiobook)
}

// maxSetParts caps a set; real ones run to a dozen or so CDs.
const maxSetParts = 50

// grabSet grabs every part of a set. The rows share a set id ("<id>/<parts>")
// so the poll can wait for all of them. If one part can't be sent, the parts
// already sent are cancelled: half a book is no use.
func (e *Engine) grabSet(ctx context.Context, bookID, libraryID int64, rel release.Release) (int64, error) {
	if len(rel.Parts) < 2 || len(rel.Parts) > maxSetParts {
		return 0, fmt.Errorf("a set has 2 to %d parts, not %d", maxSetParts, len(rel.Parts))
	}
	for _, p := range rel.Parts {
		if p.Protocol != "usenet" || p.DownloadURL == "" || len(p.Parts) > 0 {
			return 0, fmt.Errorf("every part of a set must be a usenet release")
		}
	}
	setID := fmt.Sprintf("%s/%d", auth.RandomToken()[:12], len(rel.Parts))
	var first int64
	for i, p := range rel.Parts {
		id, err := e.grab(ctx, bookID, libraryID, p, KindAudiobook)
		if err != nil {
			// the failed part's row stays, failed (and blocklisted when the
			// release is at fault); the parts already sent go
			if cerr := e.cancelSet(ctx, setID); cerr != nil {
				log.Printf("acquire: cancel set %s: %v", setID, cerr)
			}
			return id, fmt.Errorf("part %d of %d: %w", i+1, len(rel.Parts), err)
		}
		if _, err := e.db.Exec(`UPDATE queue SET set_id = ? WHERE id = ?`, setID, id); err != nil {
			log.Printf("acquire: tag set part %d: %v", id, err)
		}
		if first == 0 {
			first = id
		}
	}
	return first, nil
}

// setSize is how many parts a set id says the set has.
func setSize(setID string) int {
	i := strings.LastIndexByte(setID, '/')
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(setID[i+1:])
	if err != nil {
		return 0
	}
	return n
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

// importAudio files a finished audiobook download in the background: one
// row, or every part of a set (in part order, with their folders in the
// same order). The rows move to "importing" first, so the next poll leaves
// them alone; the SAB jobs are only cleared once the files have been taken,
// since joining a many-part book reads them for minutes.
//
// A pack grabbed for a book it turns out not to hold is a verdict on the
// release: it's blocklisted and the next one tried.
func (e *Engine) importAudio(rows []QueueItem, storages []string) {
	lead := rows[0]
	if e.ImportAudio == nil {
		for _, it := range rows {
			e.markImportFailed(it, ErrNoAudioImport)
		}
		return
	}
	if !e.startAudioImport(lead.ID) {
		return // already being imported
	}
	for _, it := range rows {
		e.setQueue(it.ID, "importing", "filing the audiobook")
	}
	go func() {
		defer e.endAudioImport(lead.ID)
		ctx := context.Background()
		err := e.ImportAudio(ctx, lead.BookID, storages)
		switch {
		case errors.Is(err, audiobook.ErrNotInDownload) && len(rows) == 1:
			e.clearSabJob(ctx, lead.ExternalID)
			e.failAndCascade(ctx, lead, err.Error())
			return
		case err != nil:
			for _, it := range rows {
				e.markImportFailed(it, err)
			}
			return
		}
		for _, it := range rows {
			e.MarkDone(it.ID)
			e.clearSabJob(ctx, it.ExternalID)
		}
	}()
}

func (e *Engine) clearSabJob(ctx context.Context, nzoID string) {
	if sab := e.Sab(); sab.Configured() {
		if err := sab.Delete(ctx, nzoID, true); err != nil {
			log.Printf("acquire: cleanup sab job %s: %v", nzoID, err)
		}
	}
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
