package epub

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

const testOPF = `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>First Ember</dc:title>
    <dc:creator opf:role="aut">Mara Voss</dc:creator>
    <dc:description>Dragons and war college.</dc:description>
    <dc:language>en</dc:language>
    <dc:publisher>Ember Tower Books</dc:publisher>
    <dc:date>2023-05-02</dc:date>
    <dc:identifier opf:scheme="ISBN">9781649374042</dc:identifier>
    <dc:identifier opf:scheme="GOODREADS">61431922</dc:identifier>
    <dc:identifier opf:scheme="HARDCOVER">433567</dc:identifier>
    <meta name="calibre:series" content="The Ember Cycle"/>
    <meta name="calibre:series_index" content="1.0"/>
  </metadata>
</package>`

func writeTestEpub(t *testing.T, opf string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "book.epub")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	files := map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"OEBPS/content.opf":      opf,
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestReadFullMetadata(t *testing.T) {
	m, err := Read(writeTestEpub(t, testOPF))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := map[string]string{
		"Title":       m.Title,
		"Description": m.Description,
		"Language":    m.Language,
		"Publisher":   m.Publisher,
		"Date":        m.Date,
		"ISBN":        m.ISBN,
		"GoodreadsID": m.GoodreadsID,
		"HardcoverID": m.HardcoverID,
		"SeriesName":  m.SeriesName,
	}
	expect := map[string]string{
		"Title":       "First Ember",
		"Description": "Dragons and war college.",
		"Language":    "en",
		"Publisher":   "Ember Tower Books",
		"Date":        "2023-05-02",
		"ISBN":        "9781649374042",
		"GoodreadsID": "61431922",
		"HardcoverID": "433567",
		"SeriesName":  "The Ember Cycle",
	}
	for k, got := range want {
		if got != expect[k] {
			t.Errorf("%s = %q, want %q", k, got, expect[k])
		}
	}
	if len(m.Authors) != 1 || m.Authors[0] != "Mara Voss" {
		t.Errorf("Authors = %v", m.Authors)
	}
	if m.SeriesIndex != 1 {
		t.Errorf("SeriesIndex = %v", m.SeriesIndex)
	}
}

func TestReadMinimalOPF(t *testing.T) {
	minimal := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Bare Book</dc:title>
    <dc:identifier>9781476735115</dc:identifier>
  </metadata>
</package>`
	m, err := Read(writeTestEpub(t, minimal))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.Title != "Bare Book" {
		t.Errorf("Title = %q", m.Title)
	}
	if m.ISBN != "9781476735115" {
		t.Errorf("bare numeric identifier should be detected as ISBN, got %q", m.ISBN)
	}
}

func TestReadRejectsNonEpub(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not.epub")
	if err := os.WriteFile(p, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err == nil {
		t.Error("expected error for non-zip file")
	}
}
