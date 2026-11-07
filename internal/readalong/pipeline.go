package readalong

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Files inside a built read-along directory.
const (
	EditionFile = "readalong.epub"
	TimingFile  = "timing.json"
	ChunkDir    = "audio"
)

// A read-along only counts when the audiobook really is this book: enough
// of what was heard must match the text (minMatch), and enough of the book
// must end up timed (minCoverage). An unrelated recording shares common
// words with any text — about a quarter of them in testing — while a real
// pairing matched two thirds even through a robotic test voice.
const (
	minMatch    = 0.45
	minCoverage = 0.25
)

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

	// cutting is quick; recognising speech is where the time goes, so it
	// gets most of the progress bar
	var words []Word
	var chunks []Chunk
	for i, sp := range spans {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before := sp.Start / probe.Duration
		share := sp.Duration / probe.Duration
		progress(fmt.Sprintf("Listening to part %d of %d", i+1, len(spans)), before)
		name := fmt.Sprintf("%04d.mp3", i+1)
		dst := filepath.Join(tmp, ChunkDir, name)
		if err := t.EncodeChunk(ctx, j.Audio, j.Format, sp, dst); err != nil {
			return nil, err
		}
		progress(fmt.Sprintf("Listening to part %d of %d", i+1, len(spans)), before+share*0.1)
		heard, err := t.Transcribe(ctx, dst, sp.Start)
		if err != nil {
			return nil, err
		}
		words = append(words, heard...)
		chunks = append(chunks, Chunk{File: name, Start: sp.Start, Duration: sp.Duration})
	}

	progress("Lining up the text", 0.99)
	timings, stats := Align(book.Units, words)
	if stats.Match < minMatch || stats.Coverage < minCoverage {
		return nil, fmt.Errorf("%w — only %.0f%% of the narration matched the text. Is it the same book and edition?",
			ErrMismatch, 100*stats.Match)
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
	// the chapter list, as an ffmetadata file ffmpeg reads alongside
	var meta strings.Builder
	meta.WriteString(";FFMETADATA1\n")
	at := 0.0
	for _, p := range parts {
		pr, err := t.Probe(ctx, p.Path, p.Format)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(p.Path), err)
		}
		end := at + pr.Duration
		fmt.Fprintf(&meta, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n",
			int64(at*1000), int64(end*1000), metaEscape(p.Title))
		at = end
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
	// the filter graph is built from input numbers only — no names in it
	var graph strings.Builder
	for i := range parts {
		graph.WriteString("[" + strconv.Itoa(i) + ":a:0]")
	}
	fmt.Fprintf(&graph, "concat=n=%d:v=0:a=1[a]", len(parts))
	metaIdx := strconv.Itoa(len(parts))
	args = append(args, "-filter_complex", graph.String(), "-map", "[a]",
		"-map_metadata", metaIdx, "-map_chapters", metaIdx,
		"-c:a", "aac", "-b:a", "96k", "-f", "ipod", "-movflags", "+faststart", "-y", "file:"+dst)
	_, err = run(ctx, t.FFmpeg, args...)
	return err
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
