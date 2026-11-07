package acquire

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/getbooky/booky/internal/audiobook"
	"github.com/getbooky/booky/internal/metadata"
)

// A book posted in parts: the two-part set is whole, the three-part one is
// missing a part, and a smaller single release ranks below the set.
const setResults = `[
	{"title":"Mara Voss - First Ember (Part 1 of 2) M4B","size":250000000,"downloadUrl":"http://x/nzb/p1","indexer":"ndx","protocol":"usenet"},
	{"title":"Mara Voss - First Ember (Part 2 of 2) M4B","size":250000000,"downloadUrl":"http://x/nzb/p2","indexer":"ndx","protocol":"usenet"},
	{"title":"Mara Voss - First Ember M4B Part 1 of 3","size":150000000,"downloadUrl":"http://x/nzb/q1","indexer":"ndx","protocol":"usenet"},
	{"title":"Mara Voss - First Ember M4B Part 3 of 3","size":150000000,"downloadUrl":"http://x/nzb/q3","indexer":"ndx","protocol":"usenet"},
	{"title":"Mara Voss - First Ember Retail MP3","size":300000000,"downloadUrl":"http://x/nzb/single","indexer":"ndx","protocol":"usenet"}]`

// sabStatus is a settable SAB history: job -> status, "Downloading" if unset.
type sabStatus struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *sabStatus) set(nzo, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[nzo] = status
}

func (s *sabStatus) get(nzo string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.m[nzo]; ok {
		return st
	}
	return "Downloading"
}

func queueOf(t *testing.T, e *Engine) []QueueItem {
	t.Helper()
	items, err := e.Queue(nil)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestSearchAudioGroupsWholeSets(t *testing.T) {
	e, _, _, bookID, _ := testEngine(t)
	f := &fakeAudioIndexers{results: setResults, jobStatus: func(string) string { return "Downloading" }}
	f.start(t, e)
	rels, _, err := e.SearchAudio(context.Background(), bookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) == 0 || len(rels[0].Parts) != 2 {
		t.Fatalf("the whole set should come first: %+v", rels)
	}
	set := rels[0]
	if !strings.HasSuffix(set.Title, "(in 2 parts)") || set.Score <= 0 || set.SizeBytes != 500000000 {
		t.Errorf("set = %q score %d size %d", set.Title, set.Score, set.SizeBytes)
	}
	if set.Parts[0].DownloadURL != "http://x/nzb/p1" || set.Parts[1].DownloadURL != "http://x/nzb/p2" {
		t.Errorf("parts out of order: %+v", set.Parts)
	}
	for _, r := range rels[1:] {
		if len(r.Parts) > 0 {
			t.Errorf("incomplete set grouped: %q", r.Title)
		}
		if strings.Contains(r.Title, "of 3") && (r.Score > 0 || !r.PartOfSet) {
			t.Errorf("a part of an incomplete set is grabbable: %+v", r)
		}
	}
}

func TestOneDownloadBeatsASetAsGoodOtherwise(t *testing.T) {
	e, _, _, bookID, _ := testEngine(t)
	f := &fakeAudioIndexers{
		results:   strings.Replace(setResults, "Retail MP3", "Retail M4B", 1),
		jobStatus: func(string) string { return "Downloading" },
	}
	f.start(t, e)
	rels, _, err := e.SearchAudio(context.Background(), bookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) < 2 || rels[0].Title != "Mara Voss - First Ember Retail M4B" || len(rels[1].Parts) != 2 {
		t.Fatalf("ranked: %+v", rels)
	}
}

func TestAudioSetWaitsForEveryPartThenJoins(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	sab := &sabStatus{}
	f := &fakeAudioIndexers{results: setResults, jobStatus: sab.get}
	f.start(t, e)
	var got [][]string
	e.ImportAudio = func(_ context.Context, id int64, paths []string) error {
		got = append(got, paths)
		return nil
	}
	ctx := context.Background()
	if ok, err := e.AutoGrabAudio(ctx, bookID, libID); err != nil || !ok {
		t.Fatalf("AutoGrabAudio = %v, %v", ok, err)
	}
	if fmt.Sprint(f.added) != "[http://x/nzb/p1 http://x/nzb/p2]" {
		t.Fatalf("sent to SAB: %q", f.added)
	}
	items := queueOf(t, e)
	if len(items) != 2 || items[0].SetID == "" || items[0].SetID != items[1].SetID || setSize(items[0].SetID) != 2 {
		t.Fatalf("queue = %+v", items)
	}
	// the set counts as in flight: no second grab
	if ok, _ := e.AutoGrabAudio(ctx, bookID, libID); !ok || len(f.added) != 2 {
		t.Fatalf("grabbed again: %q", f.added)
	}

	sab.set("nzo_1", "Completed")
	e.PollSab(ctx)
	waitImports(t, e)
	if len(got) != 0 {
		t.Fatalf("imported before every part was in: %q", got)
	}
	sab.set("nzo_2", "Completed")
	e.PollSab(ctx)
	waitImports(t, e)
	if fmt.Sprint(got) != "[[/downloads/nzo_1 /downloads/nzo_2]]" {
		t.Fatalf("imported %q", got)
	}
	if items := queueOf(t, e); len(items) != 0 {
		t.Errorf("queue left: %+v", items)
	}
	if fmt.Sprint(f.deleted) != "[nzo_1 nzo_2]" {
		t.Errorf("SAB jobs cleared: %q", f.deleted)
	}
}

func TestAudioSetPartFailureSinksTheSet(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	sab := &sabStatus{}
	f := &fakeAudioIndexers{results: setResults, jobStatus: sab.get}
	f.start(t, e)
	e.ImportAudio = func(context.Context, int64, []string) error { return nil }
	ctx := context.Background()
	if ok, err := e.AutoGrabAudio(ctx, bookID, libID); err != nil || !ok {
		t.Fatalf("AutoGrabAudio = %v, %v", ok, err)
	}
	sab.set("nzo_1", "Completed")
	sab.set("nzo_2", "Failed")
	e.PollSab(ctx)

	// part 1 is cancelled, part 2 failed and blocked, and the single
	// release grabbed in the set's place
	if len(f.added) != 3 || f.added[2] != "http://x/nzb/single" {
		t.Fatalf("sent to SAB: %q", f.added)
	}
	var failed, downloading int
	for _, it := range queueOf(t, e) {
		switch {
		case it.Status == "failed" && strings.Contains(it.ReleaseTitle, "Part 2 of 2"):
			failed++
		case it.Status == "downloading" && it.ExternalID == "nzo_3":
			downloading++
		default:
			t.Errorf("unexpected row %+v", it)
		}
	}
	if failed != 1 || downloading != 1 {
		t.Errorf("failed %d, downloading %d", failed, downloading)
	}
	if !strings.Contains(fmt.Sprint(f.deleted, f.cancelled), "nzo_1") {
		t.Errorf("part 1 left at SAB: deleted %q cancelled %q", f.deleted, f.cancelled)
	}
	rels, _, err := e.SearchAudio(ctx, bookID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rels {
		if len(r.Parts) > 0 {
			t.Errorf("the broken set is offered again: %q", r.Title)
		}
	}
}

func TestCancellingOnePartCancelsTheSet(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	f := &fakeAudioIndexers{results: setResults, jobStatus: func(string) string { return "Downloading" }}
	f.start(t, e)
	ctx := context.Background()
	if ok, err := e.AutoGrabAudio(ctx, bookID, libID); err != nil || !ok {
		t.Fatalf("AutoGrabAudio = %v, %v", ok, err)
	}
	items := queueOf(t, e)
	if err := e.Cancel(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if items := queueOf(t, e); len(items) != 0 {
		t.Errorf("queue left: %+v", items)
	}
	if len(f.cancelled) != 2 {
		t.Errorf("SAB jobs cancelled: %q", f.cancelled)
	}
	if len(f.added) != 2 {
		t.Errorf("a cancel grabbed something else: %q", f.added)
	}
}

// seriesBook adds the second book of a series to the test library.
func seriesBook(t *testing.T, e *Engine, libID int64) int64 {
	t.Helper()
	id, err := e.catalog.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "Ash and Iron", Authors: []string{"Mara Voss"},
		SeriesName: "The Ember Saga", SeriesIndex: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.catalog.AddToLibrary(id, libID, true); err != nil {
		t.Fatal(err)
	}
	return id
}

const packResults = `[
	{"title":"Mara Voss - Collected Works Books 1-5 M4B","size":2000000000,"downloadUrl":"http://x/nzb/author","indexer":"ndx","protocol":"usenet"},
	{"title":"Mara Voss - The Ember Saga Books 1-3 M4B","size":1500000000,"downloadUrl":"http://x/nzb/saga","indexer":"ndx","protocol":"usenet"},
	{"title":"Mara Voss - The Ember Saga Books 1-4 MP3","size":1800000000,"downloadUrl":"http://x/nzb/saga4","indexer":"ndx","protocol":"usenet"}]`

func TestAudioPackNamedForTheSeriesIsGrabbed(t *testing.T) {
	e, _, _, _, libID := testEngine(t)
	f := &fakeAudioIndexers{results: packResults, jobStatus: func(string) string { return "Downloading" }}
	f.start(t, e)
	book := seriesBook(t, e, libID)
	ctx := context.Background()
	rels, _, err := e.SearchAudio(ctx, book)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rels {
		if !r.Bundle {
			t.Errorf("not marked as a pack: %q", r.Title)
		}
		if strings.Contains(r.Title, "Collected") && r.Score > 0 {
			t.Errorf("a pack named only for the author is grabbable: %+v", r)
		}
	}
	if ok, err := e.AutoGrabAudio(ctx, book, libID); err != nil || !ok {
		t.Fatalf("AutoGrabAudio = %v, %v", ok, err)
	}
	if len(f.added) != 1 || !strings.Contains(f.added[0], "/saga") {
		t.Fatalf("sent to SAB: %q", f.added)
	}
}

func TestAudioSingleReleaseBeatsAPack(t *testing.T) {
	e, _, _, _, libID := testEngine(t)
	f := &fakeAudioIndexers{
		results:   strings.Replace(packResults, "[", `[{"title":"Mara Voss - Ash and Iron MP3","size":300000000,"downloadUrl":"http://x/nzb/one","indexer":"ndx","protocol":"usenet"},`, 1),
		jobStatus: func(string) string { return "Downloading" },
	}
	f.start(t, e)
	book := seriesBook(t, e, libID)
	if ok, err := e.AutoGrabAudio(context.Background(), book, libID); err != nil || !ok {
		t.Fatalf("AutoGrabAudio = %v, %v", ok, err)
	}
	if fmt.Sprint(f.added) != "[http://x/nzb/one]" {
		t.Fatalf("sent to SAB: %q", f.added)
	}
}

func TestAudioPackWithoutTheBookMovesOn(t *testing.T) {
	e, _, _, _, libID := testEngine(t)
	sab := &sabStatus{}
	f := &fakeAudioIndexers{results: packResults, jobStatus: sab.get}
	f.start(t, e)
	book := seriesBook(t, e, libID)
	e.ImportAudio = func(context.Context, int64, []string) error {
		return fmt.Errorf("checked: %w", audiobook.ErrNotInDownload)
	}
	ctx := context.Background()
	if ok, err := e.AutoGrabAudio(ctx, book, libID); err != nil || !ok {
		t.Fatalf("AutoGrabAudio = %v, %v", ok, err)
	}
	sab.set("nzo_1", "Completed")
	e.PollSab(ctx)
	waitImports(t, e)
	if len(f.added) != 2 || f.added[0] == f.added[1] {
		t.Fatalf("sent to SAB: %q", f.added)
	}
	if fmt.Sprint(f.deleted) != "[nzo_1]" {
		t.Errorf("the useless pack is left at SAB: %q", f.deleted)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM blocklist WHERE book_id = ?`, book).Scan(&n); err != nil || n != 1 {
		t.Errorf("blocklisted %d (%v)", n, err)
	}
}
