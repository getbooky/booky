package readalong

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testOPF = `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Alice</dc:title></metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="c1" href="Text/chapter%201.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="Text/ch2.xhtml" media-type="application/xhtml+xml"></item>
    <item id="css" href="style.css" media-type="text/css"/>
  </manifest>
  <spine><itemref idref="c1"/><itemref idref="c2"/></spine>
</package>`

func writeEPUB(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "book.epub")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("application/epub+zip")); err != nil {
		t.Fatal(err)
	}
	names := []string{"META-INF/container.xml", "OEBPS/content.opf", "OEBPS/nav.xhtml",
		"OEBPS/Text/chapter 1.xhtml", "OEBPS/Text/ch2.xhtml", "OEBPS/style.css"}
	for _, n := range names {
		body, ok := files[n]
		if !ok {
			continue
		}
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func sampleBook() map[string]string {
	return map[string]string{
		"META-INF/container.xml":     `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"OEBPS/content.opf":          testOPF,
		"OEBPS/nav.xhtml":            `<html xmlns="http://www.w3.org/1999/xhtml"><body><nav>Contents. Chapter one.</nav></body></html>`,
		"OEBPS/Text/chapter 1.xhtml": `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>One</title></head><body><p>Alice was tired. She sat down.</p></body></html>`,
		"OEBPS/Text/ch2.xhtml":       `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>The rabbit ran. It was late!</p></body></html>`,
		"OEBPS/style.css":            `p { margin: 0 }`,
	}
}

func TestPrepareBook(t *testing.T) {
	b, err := PrepareBook(writeEPUB(t, sampleBook()))
	if err != nil {
		t.Fatal(err)
	}
	// spine order, not manifest order; the nav isn't in the spine
	want := []string{"Alice was tired.", "She sat down.", "The rabbit ran.", "It was late!"}
	if got := texts(b.Units); !eq(got, want) {
		t.Fatalf("units %q", got)
	}
	if b.Units[0].Href != "Text/chapter%201.xhtml" || b.Units[3].ID != "booky-s4" {
		t.Fatalf("units %+v", b.Units)
	}
}

func TestWriteEdition(t *testing.T) {
	src := writeEPUB(t, sampleBook())
	b, err := PrepareBook(src)
	if err != nil {
		t.Fatal(err)
	}
	chunks := []Chunk{{File: "0001.mp3", Start: 0, Duration: 10}, {File: "0002.mp3", Start: 10, Duration: 10}}
	timings := []Timing{
		{ID: "booky-s1", Href: "Text/chapter%201.xhtml", Begin: 1, End: 3.5},
		{ID: "booky-s2", Href: "Text/chapter%201.xhtml", Begin: 3.5, End: 6},
		{ID: "booky-s3", Href: "Text/ch2.xhtml", Begin: 11.25, End: 13},
		// runs past its chunk: clipped to the chunk's end
		{ID: "booky-s4", Href: "Text/ch2.xhtml", Begin: 19, End: 21},
	}
	dst := filepath.Join(t.TempDir(), "readalong.epub")
	if err := b.WriteEdition(dst, timings, chunks); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Fatal("mimetype must stay first and stored")
	}
	read := func(name string) string {
		t.Helper()
		data, err := readEntry(&zr.Reader, name, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	opf := read("OEBPS/content.opf")
	wellFormed(t, []byte(opf))
	for _, want := range []string{
		`<item id="c1" href="Text/chapter%201.xhtml" media-type="application/xhtml+xml" media-overlay="booky-mo-c1"/>`,
		`<item id="c2" href="Text/ch2.xhtml" media-type="application/xhtml+xml" media-overlay="booky-mo-c2">`,
		`<item id="booky-mo-c1" href="booky-c1.smil" media-type="application/smil+xml"/>`,
		`<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`,
		`<meta property="media:active-class">booky-reading</meta></metadata>`,
	} {
		if !strings.Contains(opf, want) {
			t.Errorf("opf missing %s\n%s", want, opf)
		}
	}

	ch1 := read("OEBPS/Text/chapter 1.xhtml")
	wellFormed(t, []byte(ch1))
	if !strings.Contains(ch1, `<span id="booky-s1">Alice was tired.</span> <span id="booky-s2">She sat down.</span>`) {
		t.Errorf("chapter not wrapped: %s", ch1)
	}
	if css := read("OEBPS/style.css"); css != `p { margin: 0 }` {
		t.Error("untouched files must copy through unchanged")
	}

	type par struct {
		Text struct {
			Src string `xml:"src,attr"`
		} `xml:"text"`
		Audio struct {
			Src       string `xml:"src,attr"`
			ClipBegin string `xml:"clipBegin,attr"`
			ClipEnd   string `xml:"clipEnd,attr"`
		} `xml:"audio"`
	}
	var s2 struct {
		Pars []par `xml:"body>seq>par"`
	}
	if err := xml.Unmarshal([]byte(read("OEBPS/booky-c2.smil")), &s2); err != nil {
		t.Fatal(err)
	}
	if len(s2.Pars) != 2 {
		t.Fatalf("pars %+v", s2.Pars)
	}
	p := s2.Pars[0]
	if p.Text.Src != "Text/ch2.xhtml#booky-s3" || p.Audio.Src != "booky-audio/0002.mp3" ||
		p.Audio.ClipBegin != "1.250s" || p.Audio.ClipEnd != "3.000s" {
		t.Errorf("par %+v", p)
	}
	if last := s2.Pars[1]; last.Audio.ClipBegin != "9.000s" || last.Audio.ClipEnd != "10.000s" {
		t.Errorf("clipping at the chunk end: %+v", last)
	}
}

func TestWriteTiming(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "timing.json")
	err := WriteTiming(dst, []Timing{{ID: "booky-s1", Href: "a.xhtml", Begin: 1.23456, End: 2}},
		[]Chunk{{File: "0001.mp3", Duration: 61.5}}, Stats{Units: 1, TimedUnits: 1, Coverage: 1})
	if err != nil {
		t.Fatal(err)
	}
	var tf timingFile
	data, _ := os.ReadFile(dst)
	if err := json.Unmarshal(data, &tf); err != nil {
		t.Fatal(err)
	}
	if tf.Duration != 61.5 || len(tf.Units) != 1 || tf.Units[0].Begin != 1.235 {
		t.Fatalf("%+v", tf)
	}
}

func TestPrepareBookRefusesHugeDocuments(t *testing.T) {
	files := sampleBook()
	// highly compressible: a small file on disk, far over the limit unpacked
	files["OEBPS/Text/ch2.xhtml"] = "<html><body><p>" + strings.Repeat("a", maxDocSize+10) + "</p></body></html>"
	_, err := PrepareBook(writeEPUB(t, files))
	if !errors.Is(err, errTooLarge) {
		t.Fatalf("err = %v, want errTooLarge", err)
	}
}

func TestPrepareBookRejectsJunk(t *testing.T) {
	notZip := filepath.Join(t.TempDir(), "x.epub")
	if err := os.WriteFile(notZip, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareBook(notZip); err == nil {
		t.Error("a non-zip must fail")
	}
	files := sampleBook()
	delete(files, "META-INF/container.xml")
	if _, err := PrepareBook(writeEPUB(t, files)); err == nil {
		t.Error("an epub without container.xml must fail")
	}
	files = sampleBook()
	files["OEBPS/content.opf"] = strings.Replace(testOPF, `href="Text/ch2.xhtml"`, `href="https://evil.example/x.xhtml"`, 1)
	if _, err := PrepareBook(writeEPUB(t, files)); err == nil {
		t.Error("a remote manifest href must fail")
	}
}

func TestSpineFragments(t *testing.T) {
	files := sampleBook()
	// a cover image in the spine, an itemref naming nothing, and a document
	// listed twice: the engine counts the first and the third, not the second
	files["OEBPS/content.opf"] = `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata/><manifest>
<item id="cover" href="cover.jpg" media-type="image/jpeg"/>
<item id="c1" href="Text/chapter%201.xhtml" media-type="application/xhtml+xml"/>
<item id="c2" href="Text/ch2.xhtml" media-type="application/xhtml+xml"/>
</manifest><spine>
<itemref idref="cover"/><itemref idref="missing"/><itemref idref="c1"/><itemref idref="c2"/><itemref idref="c1"/>
</spine></package>`
	got, err := SpineFragments(writeEPUB(t, files))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"cover.jpg": 0, "Text/chapter%201.xhtml": 1, "Text/ch2.xhtml": 2}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if _, err := SpineFragments(filepath.Join(t.TempDir(), "nope.epub")); err == nil {
		t.Error("a missing file read as a book")
	}
}
