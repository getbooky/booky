package audiobook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/getbooky/booky/internal/readalong"
	"github.com/getbooky/booky/internal/release"
)

// ErrNotInDownload means a pack didn't hold the book it was grabbed for.
var ErrNotInDownload = errors.New("the download doesn't contain this book")

// BookRef is a library book a download might hold.
type BookRef struct {
	ID            int64
	Title, Author string
}

// Split is what a download holds, book by book.
type Split struct {
	// Bundle: the download holds several books. When false, it is one
	// book, to be imported whole as before.
	Bundle bool
	// Books are the parts of each library book found in the download.
	Books map[int64][]readalong.Part
	// Unmatched names the pieces that are no book in the library.
	Unmatched []string
}

// SplitByBook looks at a download and decides whether it is one book or a
// pack of several; for a pack, it matches each piece to a library book.
//
// A download is one book when it is a single file, a folder of chapter
// MP3s, part folders ("CD1", "Disc 2"), or parts named alike ("Book - Part
// 1.m4b", "Book - Part 2.m4b"). It is a pack when it holds two or more
// differently named book folders or M4B files, or files matching two or
// more different books. Each piece of a pack — a folder, or a file — is
// matched by its name against the candidates (the strict TitleMatches, so
// a series name doesn't pass for book one); pieces matching the same book
// are joined in order.
func SplitByBook(path string, books []BookRef) (*Split, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return &Split{}, nil
	}
	// a job folder often wraps everything in one more folder
	root := path
	for {
		entries, err := pieces(root)
		if err != nil {
			return nil, err
		}
		if len(entries) == 1 && entries[0].dir {
			root = entries[0].path
			continue
		}
		return split(entries, books)
	}
}

type piece struct {
	path, name string
	dir        bool
	format     readalong.Format // files only
}

// pieces lists a folder's audio files and subfolders, skipping hidden ones.
func pieces(dir string) ([]piece, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []piece
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		switch {
		case e.IsDir():
			if hasAudio(p) {
				out = append(out, piece{path: p, name: e.Name(), dir: true})
			}
		case e.Type().IsRegular() && audioExts[strings.ToLower(filepath.Ext(e.Name()))]:
			f, err := readalong.Detect(p)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", e.Name(), err)
			}
			out = append(out, piece{path: p, name: strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), format: f})
		}
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].name, out[j].name) })
	return out, nil
}

func hasAudio(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if d.IsDir() && p != dir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() && audioExts[strings.ToLower(filepath.Ext(p))] {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// partMarker is a part number at the end of a name: "Part 1", "- CD 2",
// "Disc 3 of 4", "(1)", " 02".
var partMarker = regexp.MustCompile(`(?i)[\s._-]*[(\[]?\s*(?:part|pt|cd|disc|disk|vol(?:ume)?)?\s*\.?\s*\d{1,3}(?:\s*(?:of|/)\s*\d{1,3})?\s*[)\]]?\s*$`)

// formatFolder is a folder named for an encoding, not a book: a download
// carrying one book twice keeps its MP3s in "mp3/" beside the M4B.
var formatFolder = regexp.MustCompile(`(?i)^(?:mp3|m4b|m4a|aac|flac|ogg|audio|audiobook|unabridged|\d{2,3}\s*kbps)$`)

// bookName is a piece's name without its part number, lowercased: the
// parts of one book share it. Format folders have none.
func bookName(name string) string {
	if formatFolder.MatchString(strings.TrimSpace(name)) {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(partMarker.ReplaceAllString(name, "")))
}

// partOf finds where a part number starts in a piece's name: "A Court of
// Mist and Fury_ Part 1 (A Court of Thorns and Roses #2)" is part 1 of A
// Court of Mist and Fury.
var partOf = regexp.MustCompile(`(?i)[\s._-]*\b(?:part|pt|cd|disc|disk)\.?\s*\d{1,3}\b.*$`)

// matchesBook reports whether a piece's name is the book's: as it stands,
// or with the part number and whatever follows it taken off.
func matchesBook(b BookRef, name string) bool {
	if release.TitleMatches(b.Title, b.Author, name) {
		return true
	}
	if bare := strings.TrimSpace(partOf.ReplaceAllString(name, "")); bare != "" && bare != name {
		return release.TitleMatches(b.Title, b.Author, bare)
	}
	return false
}

func split(entries []piece, books []BookRef) (*Split, error) {
	// what each piece matches; the longest title wins, so "A Court of
	// Thorns and Roses" doesn't take what "...Roses: Special Edition" fits
	match := make([]int64, len(entries))
	matched := map[int64]bool{}
	for i, e := range entries {
		best := -1
		for j, b := range books {
			if matchesBook(b, e.name) && (best < 0 || len(b.Title) > len(books[best].Title)) {
				best = j
			}
		}
		if best >= 0 {
			match[i] = books[best].ID
			matched[books[best].ID] = true
		}
	}
	// differently named book folders or whole-book files; chapter MP3s
	// lying loose don't count (their names differ chapter to chapter)
	names := map[string]bool{}
	for _, e := range entries {
		if e.dir || e.format == readalong.MP4 {
			if n := bookName(e.name); n != "" {
				names[n] = true
			}
		}
	}
	if len(names) < 2 && len(matched) < 2 {
		return &Split{}, nil
	}

	s := &Split{Bundle: true, Books: map[int64][]readalong.Part{}}
	for i, e := range entries {
		if match[i] == 0 {
			s.Unmatched = append(s.Unmatched, e.name)
			continue
		}
		parts, err := Parts(e.path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.name, err)
		}
		s.Books[match[i]] = append(s.Books[match[i]], parts...)
	}
	return s, nil
}
