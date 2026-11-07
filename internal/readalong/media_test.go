package readalong

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		head []byte
		want Format
	}{
		{"id3.mp3", []byte("ID3\x04\x00\x00\x00\x00\x00\x00"), MP3},
		{"frame.mp3", []byte{0xFF, 0xFB, 0x90, 0x64, 0, 0, 0, 0}, MP3},
		{"book.m4b", []byte("\x00\x00\x00\x20ftypM4B \x00\x00\x02\x00"), MP4},
		{"song.m4a", []byte("\x00\x00\x00\x1cftypM4A \x00\x00\x00\x00"), MP4},
		{"iso.mp4", []byte("\x00\x00\x00\x1cftypisom\x00\x00\x02\x00"), MP4},
		// disguised as audio, but not
		{"playlist.mp3", []byte("#EXTM3U\n#EXT-X-VERSION:3\n"), ""},
		{"concat.m4b", []byte("ffconcat version 1.0\nfile /config/booky.db\n"), ""},
		{"zip.mp3", []byte("PK\x03\x04\x14\x00\x00\x00"), ""},
		{"heic.m4b", []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"), ""},
		{"video.m4b", []byte("\x00\x00\x00\x14ftypqt  \x00\x00\x00\x00"), ""},
		{"empty.mp3", nil, ""},
		{"sync-no-layer.mp3", []byte{0xFF, 0xE0, 0, 0}, ""},
	}
	for _, c := range cases {
		p := filepath.Join(dir, c.name)
		if err := os.WriteFile(p, c.head, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Detect(p)
		if c.want == "" {
			if !errors.Is(err, ErrNotAudio) {
				t.Errorf("%s: got %q, %v — want ErrNotAudio", c.name, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v — want %q", c.name, got, err, c.want)
		}
	}
}

func TestFormatExt(t *testing.T) {
	if MP3.Ext("x.MP3") != ".mp3" || MP4.Ext("x.m4b") != ".m4b" || MP4.Ext("x.M4A") != ".m4a" || MP4.Ext("x.mp4") != ".m4a" {
		t.Fatal("extensions")
	}
}

func TestPlanChunks(t *testing.T) {
	sum := func(sp []Span) float64 {
		s := 0.0
		for _, x := range sp {
			s += x.Duration
		}
		return s
	}
	contiguous := func(t *testing.T, sp []Span, total float64) {
		t.Helper()
		at := 0.0
		for i, x := range sp {
			if math.Abs(x.Start-at) > 1e-6 {
				t.Fatalf("chunk %d starts at %.3f, want %.3f", i, x.Start, at)
			}
			if x.Duration > maxChunk+1e-6 {
				t.Fatalf("chunk %d is %.0fs, over the cap", i, x.Duration)
			}
			at += x.Duration
		}
		if math.Abs(at-total) > 1e-6 {
			t.Fatalf("chunks cover %.3f of %.3f", at, total)
		}
	}

	// no chapters: even quarter-hour parts
	p := &Probe{Duration: 3600}
	sp := PlanChunks(p)
	if len(sp) != 4 {
		t.Fatalf("got %d chunks", len(sp))
	}
	contiguous(t, sp, 3600)

	// chapters: cut at them; short ones merge, a long one splits, a short
	// tail joins the chunk before
	p = &Probe{Duration: 4800, Chapters: []Chapter{
		{Start: 0, End: 30},      // opening credits
		{Start: 30, End: 900},    // ch1
		{Start: 900, End: 1100},  // short ch2 → merged with ch3
		{Start: 1100, End: 1800}, // ch3
		{Start: 1800, End: 4700}, // long ch4 → split
		{Start: 4700, End: 4800}, // short closing → folded in
	}}
	sp = PlanChunks(p)
	contiguous(t, sp, 4800)
	if sp[0].Start != 0 || sp[1].Start != 900 {
		t.Fatalf("expected cuts at chapter marks: %+v", sp)
	}
	if sum(sp) != 4800 {
		t.Fatal("must cover the whole book")
	}

	// a short audiobook is one chunk
	sp = PlanChunks(&Probe{Duration: 120, Chapters: []Chapter{{Start: 0, End: 60}, {Start: 60, End: 120}}})
	if len(sp) != 1 || sp[0].Duration != 120 {
		t.Fatalf("short book: %+v", sp)
	}
}

func TestMetaEscape(t *testing.T) {
	if got := metaEscape("Part 1; a=b #c \\d\nnext\x00"); got != `Part 1\; a\=b \#c \\d next` {
		t.Fatalf("got %q", got)
	}
}

// ---- integration: the real tools ----

// realTools returns the installed tools, or skips: these tests need ffmpeg,
// booky-transcribe, a Vosk model and espeak-ng, as in the test image.
func realTools(t *testing.T) Tools {
	t.Helper()
	if os.Getenv("BOOKY_READALONG_IT") == "" {
		t.Skip("set BOOKY_READALONG_IT=1 to run against ffmpeg/vosk/espeak")
	}
	tools, err := FindTools()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("espeak-ng"); err != nil {
		t.Fatal("espeak-ng is needed to synthesise test speech")
	}
	return tools
}

// speak renders each sentence to its own clip and joins them, so each one's
// true start time is known.
func speak(t *testing.T, tools Tools, sentences []string, dst string) []float64 {
	t.Helper()
	dir := t.TempDir()
	var starts []float64
	var list strings.Builder
	at := 0.0
	for i, s := range sentences {
		wav := filepath.Join(dir, fmt.Sprintf("%03d.wav", i))
		if out, err := exec.Command("espeak-ng", //nolint:gosec // G204: test speech, fixed sentences
			"-s", "165", "-w", wav, s).CombinedOutput(); err != nil {
			t.Fatalf("espeak: %v %s", err, out)
		}
		pr, err := tools.Probe(context.Background(), wav, "wav")
		if err != nil {
			t.Fatal(err)
		}
		starts = append(starts, at)
		at += pr.Duration
		fmt.Fprintf(&list, "file '%s'\n", wav)
	}
	listFile := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(listFile, []byte(list.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tools.FFmpeg, //nolint:gosec // G204: test fixture, our own temp files
		"-v", "error", "-f", "concat", "-safe", "0", "-i", listFile,
		"-c:a", "libmp3lame", "-b:a", "96k", "-y", dst).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return starts
}

var aliceSentences = []string{
	"Alice was beginning to get very tired of sitting by her sister on the bank, and of having nothing to do.",
	"Once or twice she had peeped into the book her sister was reading, but it had no pictures or conversations in it.",
	"And what is the use of a book, thought Alice, without pictures or conversations?",
	"So she was considering in her own mind whether the pleasure of making a daisy chain would be worth the trouble of getting up and picking the daisies.",
	"Suddenly a White Rabbit with pink eyes ran close by her.",
	"There was nothing so very remarkable in that.",
	"Nor did Alice think it so very much out of the way to hear the Rabbit say to itself, Oh dear! Oh dear! I shall be late!",
	"But when the Rabbit actually took a watch out of its waistcoat pocket, and looked at it, and then hurried on, Alice started to her feet.",
	"Burning with curiosity, she ran across the field after it.",
	"She was just in time to see it pop down a large rabbit hole under the hedge.",
}

func aliceEPUB(t *testing.T) string {
	files := sampleBook()
	var ch1, ch2 strings.Builder
	for i, s := range aliceSentences {
		if i < 5 {
			ch1.WriteString(s + " ")
		} else {
			ch2.WriteString(s + " ")
		}
	}
	files["OEBPS/Text/chapter 1.xhtml"] = `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>` + ch1.String() + `</p></body></html>`
	files["OEBPS/Text/ch2.xhtml"] = `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>` + ch2.String() + `</p></body></html>`
	return writeEPUB(t, files)
}

func TestBuildEndToEnd(t *testing.T) {
	tools := realTools(t)
	audio := filepath.Join(t.TempDir(), "alice.mp3")
	starts := speak(t, tools, aliceSentences, audio)
	out := filepath.Join(t.TempDir(), "readalong", "1")

	var phases []string
	begin := time.Now()
	res, err := tools.Build(context.Background(), Job{
		EPUB: aliceEPUB(t), Audio: audio, Format: MP3, OutDir: out,
		Progress: func(p string, _ float64) { phases = append(phases, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("built in %v: %+v (%d progress updates)", time.Since(begin), res.Stats, len(phases))
	if res.Stats.Coverage < 0.95 {
		t.Fatalf("coverage %.2f", res.Stats.Coverage)
	}

	var tf timingFile
	data, err := os.ReadFile(filepath.Join(out, TimingFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &tf); err != nil {
		t.Fatal(err)
	}
	// a unit that opens one of the spoken sentences must start when that
	// sentence did (one sentence can hold several units: "Oh dear! Oh dear!")
	book, err := PrepareBook(aliceEPUB(t))
	if err != nil {
		t.Fatal(err)
	}
	text := map[string]string{}
	for _, u := range book.Units {
		text[u.ID] = u.Text
	}
	checked := 0
	for _, u := range tf.Units {
		for i, sentence := range aliceSentences {
			if !strings.HasPrefix(sentence, text[u.ID]) {
				continue
			}
			checked++
			if d := math.Abs(u.Begin - starts[i]); d > 0.6 {
				t.Errorf("%s (%q) begins %.2fs, true start %.2fs", u.ID, text[u.ID], u.Begin, starts[i])
			}
		}
	}
	if checked != len(aliceSentences) {
		t.Errorf("checked %d sentence starts, want %d", checked, len(aliceSentences))
	}
	for _, f := range []string{EditionFile, filepath.Join(ChunkDir, "0001.mp3")} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	// no build leftovers next to the result
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".building-*"))
	if len(left) > 0 {
		t.Errorf("leftover build dirs: %v", left)
	}

	// rebuilding replaces the old result in one piece
	if _, err := tools.Build(context.Background(), Job{EPUB: aliceEPUB(t), Audio: audio, Format: MP3, OutDir: out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out + ".old"); !os.IsNotExist(err) {
		t.Error("the replaced build should be gone")
	}
}

func TestBuildRejectsTheWrongAudiobook(t *testing.T) {
	tools := realTools(t)
	audio := filepath.Join(t.TempDir(), "other.mp3")
	speak(t, tools, []string{
		"It was the best of times, it was the worst of times.",
		"It was the age of wisdom, it was the age of foolishness.",
		"We had everything before us, we had nothing before us.",
	}, audio)
	res, err := tools.Build(context.Background(), Job{EPUB: aliceEPUB(t), Audio: audio, Format: MP3,
		OutDir: filepath.Join(t.TempDir(), "x")})
	if res != nil {
		t.Logf("accepted with %+v", res.Stats)
	}
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("err = %v, want ErrMismatch", err)
	}
}

func TestBuildCancels(t *testing.T) {
	tools := realTools(t)
	audio := filepath.Join(t.TempDir(), "alice.mp3")
	speak(t, tools, aliceSentences, audio)
	ctx, cancel := context.WithCancel(context.Background())
	out := filepath.Join(t.TempDir(), "x")
	_, err := tools.Build(ctx, Job{EPUB: aliceEPUB(t), Audio: audio, Format: MP3, OutDir: out,
		Progress: func(p string, _ float64) {
			if strings.HasPrefix(p, "Listening") {
				cancel()
			}
		}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a cancelled build must leave nothing behind")
	}
}

func TestJoin(t *testing.T) {
	tools := realTools(t)
	dir := t.TempDir()
	var parts []Part
	for i, s := range aliceSentences[:3] {
		p := filepath.Join(dir, fmt.Sprintf("%02d.mp3", i+1))
		speak(t, tools, []string{s}, p)
		parts = append(parts, Part{Path: p, Format: MP3, Title: fmt.Sprintf("Part %d; =#", i+1)})
	}
	dst := filepath.Join(dir, "book.m4b")
	if err := tools.Join(context.Background(), parts, dst); err != nil {
		t.Fatal(err)
	}
	f, err := Detect(dst)
	if err != nil || f != MP4 {
		t.Fatalf("joined file: %q %v", f, err)
	}
	pr, err := tools.Probe(context.Background(), dst, MP4)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Chapters) != 3 || pr.Chapters[0].Title != "Part 1; =#" {
		t.Fatalf("chapters: %+v", pr.Chapters)
	}
}

// A recording of only part of the book is refused, and says so — it's the
// right book, so it mustn't be called the wrong one.
func TestBuildRefusesAPartialAudiobook(t *testing.T) {
	tools := realTools(t)
	audio := filepath.Join(t.TempDir(), "part.mp3")
	speak(t, tools, aliceSentences[:1], audio)
	res, err := tools.Build(context.Background(), Job{EPUB: aliceEPUB(t), Audio: audio, Format: MP3,
		OutDir: filepath.Join(t.TempDir(), "x")})
	if res != nil {
		t.Logf("accepted with %+v", res.Stats)
	}
	if !errors.Is(err, ErrMismatch) || !strings.Contains(err.Error(), "Is the audiobook complete?") {
		t.Fatalf("err = %v, want a partial-audiobook mismatch", err)
	}
}
