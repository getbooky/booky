package readalong

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Files inside a built read-along directory.
const (
	EditionFile = "readalong.epub"
	TimingFile  = "timing.json"
	ChunkDir    = "audio"
)

// A read-along only counts when the audiobook really is this book
// (Stats.SameBook) and enough of the book ends up timed (minCoverage). Any
// two texts share a quarter of their words, so a narration that matches
// half the text is this book; but a cinematic edition, with music and
// sound effects under a full cast, can leave the recognizer matching less
// than that of the right book, and then the words' order has to show it.
const minCoverage = 0.25

// Job is one read-along build.
type Job struct {
	EPUB   string
	Audio  string
	Format Format
	// OutDir receives EditionFile, TimingFile and the ChunkDir. It is built
	// alongside and swapped in whole, so readers never see a half-built one.
	OutDir string
	// Progress, when set, hears what is happening and how far along (0–1).
	Progress func(phase string, done float64)
}

// Result summarises a finished build.
type Result struct {
	Stats    Stats
	Duration float64
	Chunks   int
}

// ErrMismatch means the audiobook and the ebook don't line up.
var ErrMismatch = errors.New("the audiobook doesn't match this ebook")

// Build makes a read-along: the audiobook cut into chunks, transcribed, and
// lined up with the ebook's sentences.
func (t Tools) Build(ctx context.Context, j Job) (*Result, error) {
	progress := func(phase string, done float64) {
		if j.Progress != nil {
			j.Progress(phase, done)
		}
	}
	progress("Reading the ebook", 0)
	book, err := PrepareBook(j.EPUB)
	if err != nil {
		return nil, err
	}
	probe, err := t.Probe(ctx, j.Audio, j.Format)
	if err != nil {
		return nil, err
	}
	spans := PlanChunks(probe)

	if err := os.MkdirAll(filepath.Dir(j.OutDir), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(j.OutDir), ".building-")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(tmp)
		}
	}()
	if err := os.Mkdir(filepath.Join(tmp, ChunkDir), 0o755); err != nil {
		return nil, err
	}

	// Each part is cut and listened to on its own, several at once: speech
	// recognition is where the time goes, and it keeps one core busy per
	// part. Results land by index, so the words come out in order however
	// the parts finish.
	chunks := make([]Chunk, len(spans))
	heard := make([][]Word, len(spans))
	workers := t.Workers
	if workers < 1 {
		workers = 1
	}
	workers = min(workers, len(spans))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		firstErr error
		doneSecs float64
		doneN    int
		wg       sync.WaitGroup
	)
	next := make(chan int)
	progress(fmt.Sprintf("Listening to %d parts", len(spans)), 0)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				sp := spans[i]
				name := fmt.Sprintf("%04d.mp3", i+1)
				dst := filepath.Join(tmp, ChunkDir, name)
				err := t.EncodeChunk(ctx, j.Audio, j.Format, sp, dst)
				var words []Word
				if err == nil {
					words, err = t.Transcribe(ctx, dst, sp.Start)
				}
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
						cancel()
					}
				} else {
					heard[i] = words
					chunks[i] = Chunk{File: name, Start: sp.Start, Duration: sp.Duration}
					doneSecs += sp.Duration
					doneN++
					progress(fmt.Sprintf("Listening: %d of %d parts done", doneN, len(spans)), doneSecs/probe.Duration)
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for i := range spans {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var words []Word
	for _, h := range heard {
		words = append(words, h...)
	}

	progress("Lining up the text", 0.99)
	timings, stats := Align(book.Units, words)
	stats.Baseline = Baseline(book.Units, words)
	switch {
	case !stats.SameBook():
		return nil, fmt.Errorf("%w — %.0f%% of the narration matched the text, about what a different book would (%.0f%%). Is it the same book and edition?",
			ErrMismatch, 100*stats.Match, 100*stats.Baseline)
	case stats.Coverage < minCoverage:
		// the narration is this book, but only a part of it
		return nil, fmt.Errorf("%w — the narration covers only %.0f%% of the book. Is the audiobook complete?",
			ErrMismatch, 100*stats.Coverage)
	}
	if err := book.WriteEdition(filepath.Join(tmp, EditionFile), timings, chunks); err != nil {
		return nil, err
	}
	if err := WriteTiming(filepath.Join(tmp, TimingFile), timings, chunks, stats); err != nil {
		return nil, err
	}

	// swap the new build in, then drop the old one
	old := j.OutDir + ".old"
	os.RemoveAll(old)
	if _, err := os.Stat(j.OutDir); err == nil {
		if err := os.Rename(j.OutDir, old); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(tmp, j.OutDir); err != nil {
		os.Rename(old, j.OutDir) //nolint:errcheck // best effort: put the old one back
		return nil, err
	}
	keep = true
	os.RemoveAll(old)
	progress("Done", 1)
	return &Result{Stats: stats, Duration: probe.Duration, Chunks: len(chunks)}, nil
}

// Part is one file of an audiobook that comes in pieces.
type Part struct {
	Path   string
	Format Format
	Title  string // chapter name; the file name works
}

// Join makes one M4B from an audiobook's parts, in order, with a chapter
// mark at the start of each part — so a book that arrived as forty MP3s sits
// next to its ebook as a single file.
func (t Tools) Join(ctx context.Context, parts []Part, dst string) error {
	if len(parts) == 0 {
		return errors.New("no audio files")
	}
	probes := make([]*Probe, len(parts))
	for i, p := range parts {
		pr, err := t.Probe(ctx, p.Path, p.Format)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(p.Path), err)
		}
		probes[i] = pr
	}
	// the chapter list, as an ffmetadata file ffmpeg reads alongside
	var meta strings.Builder
	meta.WriteString(";FFMETADATA1\n")
	for _, c := range joinedChapters(parts, probes) {
		fmt.Fprintf(&meta, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n",
			int64(c.Start*1000), int64(c.End*1000), metaEscape(c.Title))
	}
	metaFile, err := os.CreateTemp(filepath.Dir(dst), ".chapters-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(metaFile.Name())
	if _, err := metaFile.WriteString(meta.String()); err != nil {
		metaFile.Close()
		return err
	}
	if err := metaFile.Close(); err != nil {
		return err
	}

	args := []string{"-nostdin", "-hide_banner", "-v", "error"}
	for _, p := range parts {
		args = append(args, input(p.Format, p.Path)...)
	}
	args = append(args, "-protocol_whitelist", "file", "-f", "ffmetadata", "-i", "file:"+metaFile.Name())
	// The filter graph is built from input numbers only — no names in it.
	// concat needs every input alike, and parts aren't always encoded the
	// same way (a 44.1 kHz stereo part 1, a 22 kHz mono part 2), so each is
	// brought to one rate and layout first.
	rate, layout := joinFormat(probes)
	var graph strings.Builder
	for i := range parts {
		fmt.Fprintf(&graph, "[%d:a:0]aresample=%d,aformat=sample_rates=%d:channel_layouts=%s[p%d];", i, rate, rate, layout, i)
	}
	for i := range parts {
		fmt.Fprintf(&graph, "[p%d]", i)
	}
	fmt.Fprintf(&graph, "concat=n=%d:v=0:a=1[a]", len(parts))
	metaIdx := strconv.Itoa(len(parts))
	args = append(args, "-filter_complex", graph.String(), "-map", "[a]",
		"-map_metadata", metaIdx, "-map_chapters", metaIdx,
		"-c:a", "aac", "-b:a", "96k", "-f", "ipod", "-movflags", "+faststart", "-y", "file:"+dst)
	_, err = run(ctx, t.FFmpeg, args...)
	return err
}

// joinedChapters is the chapter list of parts played back to back: each
// part's own chapters, moved to where the part starts, or — for a part
// without chapters (an MP3, or an M4B with one mark) — the part as one
// chapter named after its file.
func joinedChapters(parts []Part, probes []*Probe) []Chapter {
	var out []Chapter
	at := 0.0
	for i, p := range parts {
		pr := probes[i]
		if len(pr.Chapters) > 1 {
			for _, c := range pr.Chapters {
				start, end := at+c.Start, at+math.Min(c.End, pr.Duration)
				if end <= start {
					continue
				}
				title := c.Title
				if title == "" {
					title = p.Title
				}
				out = append(out, Chapter{Title: title, Start: start, End: end})
			}
		} else {
			out = append(out, Chapter{Title: p.Title, Start: at, End: at + pr.Duration})
		}
		at += pr.Duration
	}
	return out
}

// joinFormat picks the rate and layout every part is brought to: the
// highest rate among them (48 kHz at most), stereo if any part is.
func joinFormat(probes []*Probe) (int, string) {
	rate, stereo := 0, false
	for _, p := range probes {
		if p.SampleRate > rate {
			rate = p.SampleRate
		}
		stereo = stereo || p.Channels > 1
	}
	if rate <= 0 {
		rate = 44100
	}
	if rate > 48000 {
		rate = 48000
	}
	if stereo {
		return rate, "stereo"
	}
	return rate, "mono"
}

// metaEscape escapes a value for an ffmetadata file, where = ; # \ and line
// breaks are special.
func metaEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '=', ';', '#', '\\':
			b.WriteRune('\\')
			b.WriteRune(r)
		case '\n', '\r':
			b.WriteRune(' ')
		default:
			if r >= 0x20 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
