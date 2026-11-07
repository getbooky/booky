package audiobook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/getbooky/booky/internal/readalong"
)

// maxParts caps a many-file audiobook; real ones run to a few hundred.
const maxParts = 1000

// audioExts are the names an audiobook part may have. The name only picks
// candidates out of a folder — every file's bytes are checked regardless.
var audioExts = map[string]bool{".mp3": true, ".m4b": true, ".m4a": true, ".mp4": true}

// Destination is where a book's audiobook belongs: beside its ebook, with
// the same name, so Title.epub and Title.m4b sit together.
func Destination(ebookPath string, format readalong.Format, original string) string {
	base := strings.TrimSuffix(ebookPath, filepath.Ext(ebookPath))
	return base + format.Ext(original)
}

// Parts lists the audiobook files under dir (subfolders too: CD1/, CD2/),
// in natural order, skipping anything that isn't audio. A single file is its
// own list.
func Parts(path string) ([]readalong.Part, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		f, err := readalong.Detect(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		return []readalong.Part{{Path: path, Format: f, Title: title(path)}}, nil
	}
	var rels []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != path && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		// regular files only: a symlink could lead anywhere
		if !d.Type().IsRegular() || !audioExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		rels = append(rels, rel)
		if len(rels) > maxParts {
			return fmt.Errorf("more than %d audio files in %s", maxParts, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(rels, func(i, j int) bool { return naturalLess(rels[i], rels[j]) })
	var parts []readalong.Part
	for _, rel := range rels {
		p := filepath.Join(path, rel)
		f, err := readalong.Detect(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		parts = append(parts, readalong.Part{Path: p, Format: f, Title: title(rel)})
	}
	parts = oneCopy(parts)
	if len(parts) == 0 {
		return nil, fmt.Errorf("no MP3, M4A or M4B files in %s", path)
	}
	return parts, nil
}

// sampleName matches a preview clip shipped alongside the book.
var sampleName = regexp.MustCompile(`(?i)(^|[^a-z])sample([^a-z]|$)`)

// oneCopy keeps one copy of the book from a download folder. Releases
// sometimes carry the book twice — an M4B and a folder of the same book as
// MP3s — and joining both would make it twice as long; the format with the
// most audio is kept. Preview clips ("sample.mp3") are dropped, unless
// they're all there is.
func oneCopy(parts []readalong.Part) []readalong.Part {
	var keep []readalong.Part
	for _, p := range parts {
		if !sampleName.MatchString(filepath.Base(p.Path)) {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		keep = parts
	}
	size := map[readalong.Format]int64{}
	for _, p := range keep {
		if st, err := os.Stat(p.Path); err == nil {
			size[p.Format] += st.Size()
		}
	}
	if len(size) < 2 {
		return keep
	}
	best := keep[0].Format
	for f, n := range size {
		if n > size[best] || (n == size[best] && f < best) {
			best = f
		}
	}
	var out []readalong.Part
	for _, p := range keep {
		if p.Format == best {
			out = append(out, p)
		}
	}
	return out
}

// SortParts puts uploaded parts in reading order by their titles (the
// browser sends files in whatever order it likes).
func SortParts(parts []readalong.Part) {
	sort.SliceStable(parts, func(i, j int) bool { return naturalLess(parts[i].Title, parts[j].Title) })
}

// title turns a file name into a chapter name: "03 - The Pool of Tears.mp3"
// stays readable, capped so a silly name can't bloat the metadata.
func title(name string) string {
	t := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	if r := []rune(t); len(r) > 120 {
		t = string(r[:120])
	}
	return t
}

// naturalLess orders "Part 2" before "Part 10".
func naturalLess(a, b string) bool {
	ar, br := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	for len(ar) > 0 && len(br) > 0 {
		if unicode.IsDigit(ar[0]) && unicode.IsDigit(br[0]) {
			an, arest := leadingNumber(ar)
			bn, brest := leadingNumber(br)
			if an != bn {
				if len(an) != len(bn) {
					return len(an) < len(bn)
				}
				return an < bn
			}
			ar, br = arest, brest
			continue
		}
		if ar[0] != br[0] {
			return ar[0] < br[0]
		}
		ar, br = ar[1:], br[1:]
	}
	return len(ar) < len(br)
}

// leadingNumber splits off a run of digits, without leading zeros so "007"
// and "7" compare equal by length and value.
func leadingNumber(r []rune) (string, []rune) {
	i := 0
	for i < len(r) && unicode.IsDigit(r[i]) {
		i++
	}
	n := strings.TrimLeft(string(r[:i]), "0")
	return n, r[i:]
}

// Installed is what Install put where.
type Installed struct {
	Path     string
	Format   readalong.Format
	Size     int64
	Duration float64
}

// Install puts an audiobook at its destination beside the ebook: one file is
// moved (or copied, when move is false), several are joined into one M4B.
// It works on a temporary name in the destination folder and renames into
// place, so the destination only ever holds a whole, checked file.
func Install(ctx context.Context, tools readalong.Tools, parts []readalong.Part, ebookPath string, move bool) (*Installed, error) {
	if len(parts) == 0 {
		return nil, errors.New("no audio files")
	}
	format := parts[0].Format
	if len(parts) > 1 {
		format = readalong.MP4
	}
	dst := Destination(ebookPath, format, parts[0].Path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".booky-audio-*"+filepath.Ext(dst))
	if err != nil {
		return nil, err
	}
	tmp.Close()
	done := false
	defer func() {
		if !done {
			os.Remove(tmp.Name())
		}
	}()

	if len(parts) == 1 {
		if err := place(parts[0].Path, tmp.Name(), move); err != nil {
			return nil, err
		}
	} else if err := tools.Join(ctx, parts, tmp.Name()); err != nil {
		return nil, err
	}
	// check what landed: the right kind of file, and audio ffprobe can read
	got, err := readalong.Detect(tmp.Name())
	if err != nil || got != format {
		return nil, fmt.Errorf("installed audiobook isn't %s: %v", format, err)
	}
	probe, err := tools.Probe(ctx, tmp.Name(), format)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(tmp.Name())
	if err != nil {
		return nil, err
	}
	// readable like the ebook beside it, for other apps on the same library
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { //nolint:gosec // G302: see above
		return nil, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return nil, err
	}
	done = true
	if move && len(parts) == 1 {
		os.Remove(parts[0].Path) // copied across filesystems; the move is complete
	}
	return &Installed{Path: dst, Format: format, Size: st.Size(), Duration: probe.Duration}, nil
}

// place moves src to dst (a rename when they share a filesystem), or copies
// it when move is false.
func place(src, dst string, move bool) error {
	if move {
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
