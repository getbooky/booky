package importer

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/metadata"
)

func writeEpub(t *testing.T, dir, name, opfMeta string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	files := map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"content.opf": fmt.Sprintf(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">%s</metadata>
</package>`, opfMeta),
	}
	for n, body := range files {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return p
}

func testImporter(t *testing.T) (*Importer, int64, string) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cat := catalog.New(conn)
	profile, _ := cat.EnsureDefaultProfile()
	root := t.TempDir()
	lib, err := cat.CreateLibrary("Alex", root, profile, "alex", "hash")
	if err != nil {
		t.Fatal(err)
	}
	return New(conn, cat, nil), lib, root
}

func TestScanMatchesByEmbeddedISBN(t *testing.T) {
	im, lib, root := testImporter(t)
	writeEpub(t, filepath.Join(root, "Emmett Hale"), "Burrow.epub", `
		<dc:title>Burrow</dc:title>
		<dc:creator opf:role="aut">Emmett Hale</dc:creator>
		<dc:identifier opf:scheme="ISBN">9781476735115</dc:identifier>`)

	res, err := im.Scan(context.Background(), lib, root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Scanned != 1 || res.Matched != 1 || res.Review != 0 {
		t.Fatalf("result = %+v", res)
	}

	books, _ := im.Catalog.ListBooks(0, lib, 0)
	if len(books) != 1 || books[0].Title != "Burrow" || books[0].ISBN13 != "9781476735115" {
		t.Fatalf("books = %+v", books)
	}
	if books[0].FilePath == "" || books[0].FileFormat != "epub" {
		t.Errorf("file not recorded: %+v", books[0])
	}
}

func TestScanQueuesUnmatchedForReview(t *testing.T) {
	im, lib, root := testImporter(t)
	// no identifiers, no chain configured → review queue
	writeEpub(t, root, "emmett hale - vault 1 (v5, retail).epub", `<dc:title></dc:title>`)

	res, err := im.Scan(context.Background(), lib, root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Review != 1 {
		t.Fatalf("result = %+v", res)
	}
	queue, _ := im.ReviewQueue(lib)
	if len(queue) != 1 {
		t.Fatalf("queue = %+v", queue)
	}
	if queue[0].GuessAuthor != "emmett hale" || queue[0].GuessTitle != "vault 1" {
		t.Errorf("guess = %q / %q", queue[0].GuessTitle, queue[0].GuessAuthor)
	}
}

func TestScanIsIdempotent(t *testing.T) {
	im, lib, root := testImporter(t)
	writeEpub(t, root, "book.epub", `
		<dc:title>Burrow</dc:title>
		<dc:identifier opf:scheme="ISBN">9781476735115</dc:identifier>`)

	if _, err := im.Scan(context.Background(), lib, root); err != nil {
		t.Fatal(err)
	}
	res, err := im.Scan(context.Background(), lib, root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 1 || res.Review != 0 {
		t.Fatalf("second scan should re-recognize the file: %+v", res)
	}
	books, _ := im.Catalog.ListBooks(0, lib, 0)
	if len(books) != 1 {
		t.Fatalf("duplicate book created: %+v", books)
	}
}

func TestGuessFromFilename(t *testing.T) {
	cases := map[string][2]string{
		"emmett hale - vault 1 (v5, retail).epub": {"vault 1", "emmett hale"},
		"marlow_tide_final2.epub":                 {"marlow tide", ""},
		"Project Quiet Sky.epub":                  {"Project Quiet Sky", ""},
	}
	for file, want := range cases {
		title, author := guessFromFilename("/x/" + file)
		if title != want[0] || author != want[1] {
			t.Errorf("%s → (%q, %q), want (%q, %q)", file, title, author, want[0], want[1])
		}
	}
}

// scanProvider is a minimal metadata provider for scan tests: it answers any
// search with one canonical record, standing in for Hardcover.
type scanProvider struct{ meta metadata.BookMeta }

func (p *scanProvider) Key() string { return "fake" }
func (p *scanProvider) Search(ctx context.Context, params metadata.SearchParams) ([]metadata.BookMeta, error) {
	return []metadata.BookMeta{p.meta}, nil
}

// A scanned-in book must go through the metadata pipeline like any other add:
// the embedded identifiers pick the record, but description/series/etc. come
// from the providers — not just whatever the file happened to embed.
func TestScanEnrichesMatchedBooks(t *testing.T) {
	im, lib, root := testImporter(t)
	im.Chain = metadata.NewChain(func() []string { return []string{"fake"} }, &scanProvider{
		meta: metadata.BookMeta{
			Provider: "fake", Title: "Burrow", Authors: []string{"Emmett Hale"},
			ISBN13: "9781476735115", Description: "canonical description",
			SeriesName: "Vault", SeriesIndex: 1,
		},
	})
	writeEpub(t, filepath.Join(root, "Emmett Hale"), "Burrow.epub", `
		<dc:title>Burrow</dc:title>
		<dc:creator opf:role="aut">Emmett Hale</dc:creator>
		<dc:identifier opf:scheme="ISBN">9781476735115</dc:identifier>`)

	if _, err := im.Scan(context.Background(), lib, root); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	books, _ := im.Catalog.ListBooks(0, lib, 0)
	if len(books) != 1 {
		t.Fatalf("books = %+v", books)
	}
	if books[0].Description != "canonical description" || books[0].SeriesName != "Vault" {
		t.Fatalf("scan skipped enrichment: %+v", books[0])
	}
}
