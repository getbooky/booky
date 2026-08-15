package epub

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMetaEpub(t *testing.T, meta string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "book.epub")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	files := []struct{ name, body string }{
		{"mimetype", "application/epub+zip"},
		{"META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		{"content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">` + meta + `</metadata>
  <manifest/><spine/>
</package>`},
		{"chapter1.html", "<html><body>words</body></html>"},
	}
	for _, file := range files {
		w, _ := zw.Create(file.name)
		_, _ = w.Write([]byte(file.body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

var allFields = Fields{
	"title": true, "author": true, "series": true, "description": true,
	"language": true, "publisher": true, "pubdate": true, "identifiers": true,
}

func TestWriteReplacesAndAdds(t *testing.T) {
	p := writeMetaEpub(t, `
		<dc:title>Wrong Title</dc:title>
		<dc:creator>Wrong Author</dc:creator>
		<dc:identifier id="uid">urn:uuid:1234</dc:identifier>
		<dc:identifier opf:scheme="ISBN">0000000000</dc:identifier>`)

	m := Metadata{
		Title: "First Ember", Authors: []string{"Mara Voss"},
		SeriesName: "The Ember Cycle", SeriesIndex: 1,
		Language: "en", ISBN: "9781649374042", GoodreadsID: "61431922", HardcoverID: "hc42",
	}
	if err := Write(p, m, allFields); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(p)
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if got.Title != "First Ember" || len(got.Authors) != 1 || got.Authors[0] != "Mara Voss" {
		t.Errorf("title/author = %+v", got)
	}
	if got.SeriesName != "The Ember Cycle" || got.SeriesIndex != 1 {
		t.Errorf("series = %q #%v", got.SeriesName, got.SeriesIndex)
	}
	if got.ISBN != "9781649374042" || got.GoodreadsID != "61431922" || got.HardcoverID != "hc42" {
		t.Errorf("identity triple = %q %q %q", got.ISBN, got.GoodreadsID, got.HardcoverID)
	}

	// the uuid identifier the package points at must survive
	zr, _ := zip.OpenReader(p)
	defer zr.Close()
	opfFile, _ := openZipFile(&zr.Reader, "content.opf")
	raw, _ := os.ReadFile(p)
	_ = raw
	buf := new(strings.Builder)
	if _, err := copyAll(buf, opfFile); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "urn:uuid:1234") {
		t.Error("package uuid identifier was destroyed")
	}
	if strings.Contains(buf.String(), "0000000000") {
		t.Error("stale ISBN identifier survived")
	}
	if strings.Contains(buf.String(), "Wrong Title") {
		t.Error("stale title survived")
	}
}

func TestWriteRespectsFieldToggles(t *testing.T) {
	p := writeMetaEpub(t, `<dc:title>Keep Me</dc:title>`)
	m := Metadata{Title: "New Title", ISBN: "9781649374042"}
	if err := Write(p, m, Fields{"identifiers": true}); err != nil {
		t.Fatal(err)
	}
	got, _ := Read(p)
	if got.Title != "Keep Me" {
		t.Errorf("title should be untouched, got %q", got.Title)
	}
	if got.ISBN != "9781649374042" {
		t.Errorf("isbn should be written, got %q", got.ISBN)
	}
}

func TestWriteKeepsContentDocuments(t *testing.T) {
	p := writeMetaEpub(t, `<dc:title>X</dc:title>`)
	if err := Write(p, Metadata{Title: "Y"}, Fields{"title": true}); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	f, err := openZipFile(&zr.Reader, "chapter1.html")
	if err != nil {
		t.Fatal("content document lost:", err)
	}
	f.Close()
}

func TestWriteEscapesXML(t *testing.T) {
	p := writeMetaEpub(t, `<dc:title>X</dc:title>`)
	m := Metadata{Title: `First Ember <& "Ashen Flame">`}
	if err := Write(p, m, Fields{"title": true}); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p)
	if err != nil {
		t.Fatalf("Read after escaped write: %v", err)
	}
	if got.Title != `First Ember <& "Ashen Flame">` {
		t.Errorf("round-trip = %q", got.Title)
	}
}

func copyAll(dst *strings.Builder, src interface{ Read([]byte) (int, error) }) (int64, error) {
	buf := make([]byte, 32*1024)
	var n int64
	for {
		r, err := src.Read(buf)
		dst.Write(buf[:r])
		n += int64(r)
		if err != nil {
			if err.Error() == "EOF" {
				return n, nil
			}
			return n, err
		}
	}
}
