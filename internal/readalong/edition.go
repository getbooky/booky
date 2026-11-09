package readalong

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Book is an EPUB opened for a read-along: its content documents with every
// sentence wrapped and numbered, ready to be timed and written back out.
type Book struct {
	src     string
	opfName string // zip path of the OPF
	opfDir  string // zip directory the OPF's hrefs are relative to
	opf     []byte
	spine   []spineDoc
	docs    map[string][]byte // zip name → wrapped document
	Units   []Unit
}

type spineDoc struct {
	id, href, zipName string
}

const (
	maxDocSize  = 32 << 20 // one content document
	maxTextSize = 96 << 20 // all content documents together
	maxBookSize = 1 << 30  // everything copied into the edition
)

// errTooLarge stops a crafted EPUB (a zip bomb) from expanding without bound.
var errTooLarge = errors.New("epub content is larger than any real book")

// PrepareBook opens the EPUB at epubPath and wraps the sentences of every
// spine document, in reading order.
func PrepareBook(epubPath string) (*Book, error) {
	zr, err := zip.OpenReader(epubPath)
	if err != nil {
		return nil, fmt.Errorf("open epub: %w", err)
	}
	defer zr.Close()
	b := &Book{src: epubPath, docs: map[string][]byte{}}

	if b.opfName, b.opf, err = readOPF(&zr.Reader); err != nil {
		return nil, err
	}
	b.opfDir = path.Dir(b.opfName)
	if b.opfDir == "." {
		b.opfDir = ""
	}
	pkg, err := parsePackage(b.opf)
	if err != nil {
		return nil, err
	}
	byID := map[string]int{}
	for i, it := range pkg.Items {
		byID[it.ID] = i
	}
	next := 1
	textTotal := 0
	for _, ref := range pkg.Spine {
		i, ok := byID[ref.IDRef]
		if !ok {
			continue
		}
		it := pkg.Items[i]
		if it.MediaType != "application/xhtml+xml" {
			continue
		}
		name, err := b.zipName(it.Href)
		if err != nil {
			return nil, err
		}
		if _, done := b.docs[path.Clean(name)]; done {
			continue // listed twice in the spine
		}
		raw, err := readEntry(&zr.Reader, name, maxDocSize)
		if err != nil {
			return nil, err
		}
		// the text of a whole book is a few megabytes; all of it is held
		// in memory here, so a file built of many huge chapters is refused
		if textTotal += len(raw); textTotal > maxTextSize {
			return nil, fmt.Errorf("the ebook's text is over %d MB — too large to line up", maxTextSize>>20)
		}
		wrapped, units, n, err := WrapSentences(raw, it.Href, next)
		if err != nil {
			return nil, err
		}
		next = n
		b.docs[path.Clean(name)] = wrapped
		b.spine = append(b.spine, spineDoc{id: it.ID, href: it.Href, zipName: name})
		b.Units = append(b.Units, units...)
	}
	if len(b.Units) == 0 {
		return nil, errors.New("epub has no text to read along with")
	}
	return b, nil
}

// opfPackage is the slice of the package document read here.
type opfPackage struct {
	Items []struct {
		ID        string `xml:"id,attr"`
		Href      string `xml:"href,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"manifest>item"`
	Spine []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

// readOPF finds and reads the package document.
func readOPF(zr *zip.Reader) (name string, data []byte, err error) {
	container, err := readEntry(zr, "META-INF/container.xml", 1<<20)
	if err != nil {
		return "", nil, err
	}
	var c struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(container, &c); err != nil || len(c.Rootfiles) == 0 {
		return "", nil, errors.New("epub has no package document")
	}
	name = c.Rootfiles[0].FullPath
	data, err = readEntry(zr, name, 4<<20)
	return name, data, err
}

func parsePackage(data []byte) (*opfPackage, error) {
	var pkg opfPackage
	if err := xml.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("read package document: %w", err)
	}
	return &pkg, nil
}

// SpineFragments numbers the book's content documents the way KOReader's
// engine does when it merges them into one document: every spine entry
// that names a manifest item counts, from 0. The engine renames each id to
// "_doc_fragment_<n>_ <id>" with this n, so a device needs it to find a
// sentence. Keys are manifest hrefs, as the timing file names documents;
// a document listed twice keeps its first number.
func SpineFragments(epubPath string) (map[string]int, error) {
	zr, err := zip.OpenReader(epubPath)
	if err != nil {
		return nil, fmt.Errorf("open epub: %w", err)
	}
	defer zr.Close()
	_, data, err := readOPF(&zr.Reader)
	if err != nil {
		return nil, err
	}
	pkg, err := parsePackage(data)
	if err != nil {
		return nil, err
	}
	hrefOf := map[string]string{}
	for _, it := range pkg.Items {
		hrefOf[it.ID] = it.Href
	}
	out := map[string]int{}
	n := 0
	for _, ref := range pkg.Spine {
		href, ok := hrefOf[ref.IDRef]
		if !ok {
			continue // the engine skips these too, without counting them
		}
		if _, seen := out[href]; !seen {
			out[href] = n
		}
		n++
	}
	return out, nil
}

// zipName resolves a manifest href (relative to the OPF, URL-encoded) to the
// zip entry it names.
func (b *Book) zipName(href string) (string, error) {
	u, err := url.Parse(href)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "", fmt.Errorf("unsupported manifest href %q", href)
	}
	return path.Join(b.opfDir, u.Path), nil
}

// readEntry reads one zip entry, matched the lenient way the metadata
// reader does ("./OEBPS/x" is "OEBPS/x"), up to limit bytes.
func readEntry(zr *zip.Reader, name string, limit int64) ([]byte, error) {
	var entry *zip.File
	for _, f := range zr.File {
		if path.Clean(f.Name) == path.Clean(name) {
			entry = f
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("epub is missing %s", name)
	}
	f, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: %w", name, errTooLarge)
	}
	return data, nil
}

// Chunk is one piece of the audiobook as served to readers: an MP3 cut at a
// chapter boundary where the audiobook has chapters.
type Chunk struct {
	File     string  `json:"file"` // e.g. "0003.mp3"
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
}

// AudioDir is where the edition's SMIL files expect the chunks, relative to
// the OPF. The chunks are not inside the EPUB — at tens of megabytes each
// they'd make it far too big to open — so a reader fetches them from Booky
// by name instead.
const AudioDir = "booky-audio"

// chunkAt finds the chunk playing at second t.
func chunkAt(chunks []Chunk, t float64) int {
	i := sort.Search(len(chunks), func(i int) bool { return chunks[i].Start+chunks[i].Duration > t })
	if i == len(chunks) {
		return len(chunks) - 1
	}
	return i
}

// WriteEdition writes the read-along EPUB to dst: the original with the
// wrapped documents, one SMIL media overlay per document that has timed
// text, and the manifest entries tying them together.
func (b *Book) WriteEdition(dst string, timings []Timing, chunks []Chunk) error {
	if len(chunks) == 0 {
		return errors.New("no audio chunks")
	}
	// timings grouped by content document, in reading order
	byHref := map[string][]Timing{}
	for _, t := range timings {
		byHref[t.Href] = append(byHref[t.Href], t)
	}
	smils := map[string][]byte{} // spine id → SMIL
	for _, d := range b.spine {
		if ts := byHref[d.href]; len(ts) > 0 {
			smils[d.id] = smil(d.href, ts, chunks)
		}
	}
	opf, err := addOverlays(b.opf, smils)
	if err != nil {
		return err
	}

	zr, err := zip.OpenReader(b.src)
	if err != nil {
		return err
	}
	defer zr.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	fail := func(err error) error {
		zw.Close()
		f.Close()
		os.Remove(dst)
		return err
	}
	var copied int64
	for _, e := range zr.File {
		var body []byte
		switch name := path.Clean(e.Name); {
		case name == path.Clean(b.opfName):
			body = opf
		case b.docs[name] != nil:
			body = b.docs[name]
		}
		hdr := &zip.FileHeader{Name: e.Name, Method: e.Method, Modified: e.Modified}
		if e.Name == "mimetype" {
			hdr.Method = zip.Store // must stay first and uncompressed
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return fail(err)
		}
		if body != nil {
			if _, err := w.Write(body); err != nil {
				return fail(err)
			}
			continue
		}
		r, err := e.Open()
		if err != nil {
			return fail(err)
		}
		n, err := io.Copy(w, io.LimitReader(r, maxBookSize-copied+1))
		r.Close()
		if err != nil {
			return fail(err)
		}
		if copied += n; copied > maxBookSize {
			return fail(errTooLarge)
		}
	}
	for _, d := range b.spine {
		s, ok := smils[d.id]
		if !ok {
			continue
		}
		w, err := zw.Create(path.Join(b.opfDir, smilName(d.id)))
		if err != nil {
			return fail(err)
		}
		if _, err := w.Write(s); err != nil {
			return fail(err)
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		os.Remove(dst)
		return err
	}
	return f.Close()
}

// smilName names a document's overlay after its manifest id, which is
// unique in the package; anything outside a safe set becomes '_'.
func smilName(id string) string {
	return "booky-" + unsafeNameChars.ReplaceAllString(id, "_") + ".smil"
}

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// smil renders one document's media overlay. It sits next to the OPF, so the
// document is referenced by its manifest href and the audio by AudioDir.
func smil(href string, ts []Timing, chunks []Chunk) []byte {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<smil xmlns="http://www.w3.org/ns/SMIL" xmlns:epub="http://www.idpf.org/2007/ops" version="3.0"><body>` + "\n")
	fmt.Fprintf(&buf, `<seq epub:textref="%s">`+"\n", xmlEscape(href))
	for _, t := range ts {
		c := chunks[chunkAt(chunks, t.Begin)]
		begin := t.Begin - c.Start
		end := min(t.End, c.Start+c.Duration) - c.Start
		if end <= begin {
			end = begin + 0.001
		}
		fmt.Fprintf(&buf, `<par id="p-%s"><text src="%s#%s"/><audio src="%s/%s" clipBegin="%.3fs" clipEnd="%.3fs"/></par>`+"\n",
			t.ID, xmlEscape(href), t.ID, AudioDir, c.File, begin, end)
	}
	buf.WriteString("</seq>\n</body></smil>\n")
	return buf.Bytes()
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(b.String(), `"`, "&#34;")
}

var (
	itemTag     = regexp.MustCompile(`<(?:[A-Za-z_][\w.-]*:)?item\b[^>]*>`)
	idAttr      = regexp.MustCompile(`\bid\s*=\s*["']([^"']*)["']`)
	manifestEnd = regexp.MustCompile(`</(?:[A-Za-z_][\w.-]*:)?manifest>`)
	metadataEnd = regexp.MustCompile(`</(?:[A-Za-z_][\w.-]*:)?metadata>`)
)

// ActiveClass marks the sentence being read aloud. The edition declares it
// (media:active-class), so any EPUB 3 reader highlights the same way.
const ActiveClass = "booky-reading"

// addOverlays adds a manifest item for each SMIL and points its content
// document at it with media-overlay. The OPF is edited as text, like the
// metadata writer does, so everything else in it stays byte for byte.
func addOverlays(opf []byte, smils map[string][]byte) ([]byte, error) {
	out := itemTag.ReplaceAllFunc(opf, func(tag []byte) []byte {
		m := idAttr.FindSubmatch(tag)
		if m == nil {
			return tag
		}
		if _, ok := smils[string(m[1])]; !ok || bytes.Contains(tag, []byte("media-overlay")) {
			return tag
		}
		at := len(tag) - 1
		if tag[at-1] == '/' {
			at--
		}
		return append(append(append([]byte{}, tag[:at]...), fmt.Sprintf(` media-overlay="booky-mo-%s"`, xmlEscape(string(m[1])))...), tag[at:]...)
	})
	if m := metadataEnd.FindIndex(out); m != nil && !bytes.Contains(out, []byte("media:active-class")) {
		out = append(append(append([]byte{}, out[:m[0]]...),
			`<meta property="media:active-class">`+ActiveClass+`</meta>`...), out[m[0]:]...)
	}
	end := manifestEnd.FindIndex(out)
	if end == nil {
		return nil, errors.New("package document has no manifest")
	}
	ids := make([]string, 0, len(smils))
	for id := range smils {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var items bytes.Buffer
	for _, id := range ids {
		fmt.Fprintf(&items, `<item id="booky-mo-%s" href="%s" media-type="application/smil+xml"/>`+"\n",
			xmlEscape(id), smilName(id))
	}
	return append(append(append([]byte{}, out[:end[0]]...), items.Bytes()...), out[end[0]:]...), nil
}

// Timing file: what a device needs to follow the narration without parsing
// SMIL — every timed unit with its document, in reading order, and the
// chunks to play.
type timingFile struct {
	Version  int          `json:"version"`
	Duration float64      `json:"duration"`
	Chunks   []Chunk      `json:"chunks"`
	Units    []timingUnit `json:"units"`
	Stats    Stats        `json:"stats"`
}

type timingUnit struct {
	ID    string  `json:"id"`
	Href  string  `json:"href"`
	Begin float64 `json:"begin"`
	End   float64 `json:"end"`
}

// WriteTiming writes the timing file to dst.
func WriteTiming(dst string, timings []Timing, chunks []Chunk, stats Stats) error {
	tf := timingFile{Version: 1, Chunks: chunks, Stats: stats}
	if n := len(chunks); n > 0 {
		tf.Duration = round3(chunks[n-1].Start + chunks[n-1].Duration)
	}
	for _, t := range timings {
		tf.Units = append(tf.Units, timingUnit{ID: t.ID, Href: t.Href, Begin: round3(t.Begin), End: round3(t.End)})
	}
	data, err := json.Marshal(tf)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func round3(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }
