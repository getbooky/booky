package epub

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteEmbedsCover(t *testing.T) {
	p := writeMetaEpub(t, `<dc:title>X</dc:title>`)
	cover := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(cover, []byte("jpeg-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := Metadata{Title: "First Ember", CoverPath: cover}
	if err := Write(p, m, Fields{"title": true, "cover": true}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	img, err := openZipFile(&zr.Reader, "booky-cover.jpg")
	if err != nil {
		t.Fatal("cover image missing from epub:", err)
	}
	body, _ := io.ReadAll(img)
	img.Close()
	if string(body) != "jpeg-bytes" {
		t.Errorf("cover bytes = %q", body)
	}

	opf, _ := openZipFile(&zr.Reader, "content.opf")
	raw, _ := io.ReadAll(opf)
	opf.Close()
	if !strings.Contains(string(raw), `<meta name="cover" content="booky-cover"/>`) {
		t.Error("cover meta missing")
	}
	if !strings.Contains(string(raw), `id="booky-cover"`) {
		t.Error("manifest item missing")
	}

	// writing again must replace, not duplicate
	if err := Write(p, m, Fields{"cover": true}); err != nil {
		t.Fatal(err)
	}
	zr2, _ := zip.OpenReader(p)
	defer zr2.Close()
	count := 0
	for _, f := range zr2.File {
		if f.Name == "booky-cover.jpg" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("cover entries = %d, want 1", count)
	}
}

func TestWriteSkipsCoverWhenToggleOff(t *testing.T) {
	p := writeMetaEpub(t, `<dc:title>X</dc:title>`)
	cover := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(cover, []byte("jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, Metadata{Title: "Y", CoverPath: cover}, Fields{"title": true}); err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.OpenReader(p)
	defer zr.Close()
	if _, err := openZipFile(&zr.Reader, "booky-cover.jpg"); err == nil {
		t.Error("cover should not be embedded when the toggle is off")
	}
}

// TestCoverRoundTrip: a cover Booky wrote into an epub can be extracted
// back out — that's what lets scanned shelves show covers with no
// provider URL at all.
func TestCoverRoundTrip(t *testing.T) {
	p := writeMetaEpub(t, `<dc:title>X</dc:title>`)
	cover := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(cover, []byte("jpeg-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, Metadata{Title: "X", CoverPath: cover}, Fields{"cover": true}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, mediaType, err := Cover(p)
	if err != nil {
		t.Fatalf("Cover: %v", err)
	}
	if string(data) != "jpeg-bytes" {
		t.Errorf("extracted bytes = %q", data)
	}
	if mediaType != "image/jpeg" {
		t.Errorf("media type = %q", mediaType)
	}
}

func TestCoverMissing(t *testing.T) {
	p := writeMetaEpub(t, `<dc:title>X</dc:title>`)
	if _, _, err := Cover(p); err == nil {
		t.Fatal("expected an error for an epub with no cover")
	}
}
