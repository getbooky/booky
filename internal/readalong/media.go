package readalong

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Audio files are untrusted input, so ffmpeg never decides for itself what
// it is reading: every call names the container (Format, sniffed from the
// file's first bytes) and allows only local files. Left to probe, ffmpeg
// would happily follow a "audiobook" that is really an HLS playlist or a
// concat script to other files on the server, or out to the network.

// Format is an accepted audio container, by ffmpeg demuxer name.
type Format string

const (
	MP3 Format = "mp3"
	MP4 Format = "mov" // .m4b .m4a .mp4 — ffmpeg's mov/mp4 demuxer
)

// Ext is the file extension a format is stored under.
func (f Format) Ext(original string) string {
	if f == MP3 {
		return ".mp3"
	}
	switch strings.ToLower(filepath.Ext(original)) {
	case ".m4a", ".mp4":
		return ".m4a"
	}
	return ".m4b"
}

// ErrNotAudio means a file isn't an audiobook format Booky accepts.
var ErrNotAudio = errors.New("not an MP3, M4A or M4B audio file")

// Detect sniffs a file's container from its first bytes, ignoring its name.
func Detect(path string) (Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 12)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		brand := string(head[8:12])
		// audio brands only — not a video, not a HEIC picture
		switch brand {
		case "M4A ", "M4B ", "mp42", "mp41", "isom", "iso2":
			return MP4, nil
		}
		return "", ErrNotAudio
	case len(head) >= 3 && string(head[:3]) == "ID3":
		return MP3, nil
	case len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0 && head[1]&0x06 != 0:
		return MP3, nil // a bare MPEG audio frame (layer bits set)
	}
	return "", ErrNotAudio
}

// Tools are the programs the pipeline runs.
type Tools struct {
	FFmpeg, FFprobe, Transcriber string
	Model                        string // Vosk model directory
	// Workers is how many parts of an audiobook are listened to at once
	// (0 or 1: one at a time).
	Workers int
}

// DefaultWorkers is half the CPU cores, at most four: speech recognition
// keeps a core busy for each part, and the server has other work to do.
// Each worker holds a copy of the speech model (a few hundred MB).
func DefaultWorkers() int {
	n := runtime.NumCPU() / 2
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	return n
}

// DefaultModel is where Alpine's vosk-model-small-en-us package installs.
const DefaultModel = "/usr/share/vosk-models/small-en-us"

// FindTools locates the programs on PATH. BOOKY_VOSK_MODEL overrides the
// model, for a bigger or another language's.
func FindTools() (Tools, error) {
	t := Tools{Model: DefaultModel, Workers: DefaultWorkers()}
	if m := os.Getenv("BOOKY_VOSK_MODEL"); m != "" {
		t.Model = m
	}
	// BOOKY_READALONG_WORKERS sets how many parts are listened to at once
	if n, err := strconv.Atoi(os.Getenv("BOOKY_READALONG_WORKERS")); err == nil && n >= 1 {
		t.Workers = min(n, runtime.NumCPU())
	}
	var missing []string
	for _, p := range []struct {
		name string
		dst  *string
	}{{"ffmpeg", &t.FFmpeg}, {"ffprobe", &t.FFprobe}, {"booky-transcribe", &t.Transcriber}} {
		path, err := exec.LookPath(p.name)
		if err != nil {
			missing = append(missing, p.name)
			continue
		}
		*p.dst = path
	}
	if st, err := os.Stat(t.Model); err != nil || !st.IsDir() { //nolint:gosec // G703: the model path is the server's own configuration
		missing = append(missing, "speech model at "+t.Model)
	}
	if len(missing) > 0 {
		return t, fmt.Errorf("read-along needs %s (they ship in the Docker image)", strings.Join(missing, ", "))
	}
	return t, nil
}

// input is how every ffmpeg/ffprobe call names a file: container forced,
// local files only, and a "file:" URL so a colon in a name can't select a
// protocol.
func input(f Format, path string) []string {
	return []string{"-protocol_whitelist", "file,pipe", "-f", string(f), "-i", "file:" + path}
}

// run runs a tool, returning a short error with the tail of its stderr.
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: our own tools, found at startup; arguments are argv, never a shell
	var stdout bytes.Buffer
	stderr := tailBuffer{max: 2048}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// tailBuffer keeps only the last max bytes written — enough of a tool's
// stderr to explain a failure without holding a runaway log.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// Chapter is a chapter mark in an audiobook.
type Chapter struct {
	Title      string
	Start, End float64
}

// Probe is what Booky needs to know about an audiobook file.
type Probe struct {
	Duration float64
	Chapters []Chapter
	// the first audio stream's sample rate and channel count (0 if unknown)
	SampleRate int
	Channels   int
}

// Probe reads an audiobook's length and chapter marks.
func (t Tools) Probe(ctx context.Context, path string, f Format) (*Probe, error) {
	args := append([]string{"-v", "error", "-print_format", "json", "-show_format", "-show_chapters",
		"-show_streams", "-select_streams", "a:0"},
		"-protocol_whitelist", "file,pipe", "-f", string(f), "file:"+path)
	out, err := run(ctx, t.FFprobe, args...)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Chapters []struct {
			Start string            `json:"start_time"`
			End   string            `json:"end_time"`
			Tags  map[string]string `json:"tags"`
		} `json:"chapters"`
		Streams []struct {
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}
	p := &Probe{}
	if len(raw.Streams) > 0 {
		p.SampleRate, _ = strconv.Atoi(raw.Streams[0].SampleRate)
		p.Channels = raw.Streams[0].Channels
	}
	p.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	if p.Duration <= 0 || math.IsNaN(p.Duration) || math.IsInf(p.Duration, 0) {
		return nil, errors.New("audio has no readable duration")
	}
	// the longest audiobooks run a few days; a header claiming years would
	// have everything downstream plan for it
	if p.Duration > maxDuration {
		return nil, fmt.Errorf("audio claims to be %.0f hours long", p.Duration/3600)
	}
	for _, c := range raw.Chapters {
		s, err1 := strconv.ParseFloat(c.Start, 64)
		e, err2 := strconv.ParseFloat(c.End, 64)
		if err1 == nil && err2 == nil && e > s {
			p.Chapters = append(p.Chapters, Chapter{Title: c.Tags["title"], Start: s, End: e})
		}
	}
	return p, nil
}

const (
	minChunk    = 5 * 60.0  // chapters shorter than this are merged
	maxChunk    = 30 * 60.0 // a chapter longer than this is split…
	targetChunk = 15 * 60.0 // …into parts about this long
)

// Span is a stretch of the audiobook, in seconds.
type Span struct{ Start, Duration float64 }

// PlanChunks decides how the audiobook is cut for listening: at chapter
// marks where it has them (short chapters merged, long ones split), in even
// quarter-hour parts where it doesn't. Chunks keep each download small
// enough to start playing quickly, on a phone or an e-reader.
func PlanChunks(p *Probe) []Span {
	var marks []float64 // cut points, including 0 and the end
	marks = append(marks, 0)
	last := 0.0
	for _, c := range p.Chapters {
		if c.Start > last+0.5 && c.Start < p.Duration-0.5 && c.Start-last >= minChunk {
			marks = append(marks, c.Start)
			last = c.Start
		}
	}
	if p.Duration-last < minChunk && len(marks) > 1 {
		marks = marks[:len(marks)-1] // fold a short tail into the chunk before
	}
	marks = append(marks, p.Duration)

	var out []Span
	for i := 0; i+1 < len(marks); i++ {
		start, length := marks[i], marks[i+1]-marks[i]
		parts := 1
		if length > maxChunk || len(p.Chapters) == 0 {
			parts = int(math.Ceil(length / targetChunk))
		}
		if parts < 1 {
			parts = 1
		}
		step := length / float64(parts)
		for k := 0; k < parts; k++ {
			out = append(out, Span{Start: start + step*float64(k), Duration: step})
		}
	}
	return out
}

// EncodeChunk cuts span out of the audiobook as a mono 64 kbps MP3 — plenty
// for a voice, small to stream, and a format every player decodes.
func (t Tools) EncodeChunk(ctx context.Context, src string, f Format, sp Span, dst string) error {
	args := []string{"-nostdin", "-hide_banner", "-v", "error",
		"-ss", strconv.FormatFloat(sp.Start, 'f', 3, 64), "-t", strconv.FormatFloat(sp.Duration, 'f', 3, 64)}
	args = append(args, input(f, src)...)
	args = append(args, "-map", "0:a:0", "-vn", "-map_metadata", "-1", "-ac", "1", "-ar", "44100",
		"-c:a", "libmp3lame", "-b:a", "64k", "-f", "mp3", "-y", "file:"+dst)
	_, err := run(ctx, t.FFmpeg, args...)
	return err
}

// Devices that can't decode compressed audio (a Kindle's sound path takes
// raw PCM only) are sent chunks decoded to this: signed 16-bit little-endian,
// mono, at DeviceRate. Speech loses nothing audible at this rate, and it
// halves the bytes of 44.1 kHz.
const (
	DeviceRate        = 22050
	DeviceBytesPerSec = DeviceRate * 2
)

// DecodePCM streams a chunk to w as raw PCM in the device format.
func (t Tools) DecodePCM(ctx context.Context, chunk string, w io.Writer) error {
	args := append(append([]string{"-nostdin", "-hide_banner", "-v", "error"},
		input(MP3, chunk)...), "-map", "0:a:0", "-vn", "-ac", "1", "-ar", strconv.Itoa(DeviceRate), "-f", "s16le", "pipe:1")
	cmd := exec.CommandContext(ctx, t.FFmpeg, args...) //nolint:gosec // G204: see run
	stderr := tailBuffer{max: 2048}
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Transcribe turns one chunk's speech into words, with times shifted by
// offset so they count from the start of the whole audiobook.
func (t Tools) Transcribe(ctx context.Context, chunk string, offset float64) ([]Word, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := append(append([]string{"-nostdin", "-hide_banner", "-v", "error"},
		input(MP3, chunk)...), "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
	decode := exec.CommandContext(ctx, t.FFmpeg, args...)     //nolint:gosec // G204: see run
	recog := exec.CommandContext(ctx, t.Transcriber, t.Model) //nolint:gosec // G204: see run
	pcm, err := decode.StdoutPipe()
	if err != nil {
		return nil, err
	}
	recog.Stdin = pcm
	out, err := recog.StdoutPipe()
	if err != nil {
		return nil, err
	}
	decodeErr := tailBuffer{max: 1024}
	recogErr := tailBuffer{max: 1024}
	decode.Stderr = &decodeErr
	recog.Stderr = &recogErr
	if err := decode.Start(); err != nil {
		return nil, err
	}
	if err := recog.Start(); err != nil {
		cancel()
		_ = decode.Wait()
		return nil, err
	}

	var words []Word
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var r struct {
			Result []struct {
				Word  string  `json:"word"`
				Start float64 `json:"start"`
				End   float64 `json:"end"`
			} `json:"result"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue // not a result line
		}
		for _, w := range r.Result {
			words = append(words, Word{Text: w.Word, Start: offset + w.Start, End: offset + w.End})
		}
	}
	scanErr := sc.Err()
	recogWait := recog.Wait()
	decodeWait := decode.Wait()
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case decodeWait != nil:
		return nil, fmt.Errorf("decode %s: %w: %s", filepath.Base(chunk), decodeWait, strings.TrimSpace(decodeErr.String()))
	case recogWait != nil:
		return nil, fmt.Errorf("transcribe %s: %w: %s", filepath.Base(chunk), recogWait, strings.TrimSpace(recogErr.String()))
	case scanErr != nil:
		return nil, scanErr
	}
	return words, nil
}

// maxDuration is the longest audio accepted, in seconds: 500 hours.
const maxDuration = 500 * 3600
