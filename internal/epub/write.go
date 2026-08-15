package epub

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Fields selects which metadata groups Write replaces inside the file.
// Keys follow the metadata_write setting: title, author, series,
// description, language, publisher, pubdate, identifiers.
type Fields map[string]bool

// Write rewrites the EPUB's OPF so the file itself carries Booky's metadata —
// including the identity triple (ISBN / Goodreads / Hardcover) and calibre
// series conventions that KoReader understands. Only the groups enabled in
// fields are touched; everything else in the OPF is preserved byte-for-byte.
// The rewrite is atomic: a temp file replaces the original only on success.
func Write(fsPath string, m Metadata, fields Fields) error {
	zr, err := zip.OpenReader(fsPath)
	if err != nil {
		return fmt.Errorf("open epub: %w", err)
	}
	defer zr.Close()

	opfName, err := opfPath(&zr.Reader)
	if err != nil {
		return err
	}
	opfFile, err := openZipFile(&zr.Reader, opfName)
	if err != nil {
		return err
	}
	opfRaw, err := io.ReadAll(io.LimitReader(opfFile, maxOPFSize))
	opfFile.Close()
	if err != nil {
		return err
	}

	newOPF, err := rewriteOPF(string(opfRaw), m, fields)
	if err != nil {
		return err
	}

	// cover: embed the cached image next to the OPF and point the standard
	// <meta name="cover"> + manifest entry at it, replacing any earlier one
	var coverBytes []byte
	var coverZipPath string
	if fields["cover"] && m.CoverPath != "" {
		if img, err := os.ReadFile(m.CoverPath); err == nil && len(img) > 0 {
			if patched, ok := addCoverToOPF(newOPF); ok {
				newOPF = patched
				coverBytes = img
				if dir := path.Dir(opfName); dir != "." {
					coverZipPath = dir + "/" + coverFileName
				} else {
					coverZipPath = coverFileName
				}
			}
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(fsPath), ".booky-meta-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	zw := zip.NewWriter(tmp)
	for _, f := range zr.File {
		if path.Clean(f.Name) == path.Clean(opfName) {
			continue
		}
		if coverZipPath != "" && path.Clean(f.Name) == path.Clean(coverZipPath) {
			continue // replaced below
		}
		// mimetype must stay first and uncompressed per the EPUB spec
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method})
		if err != nil {
			return err
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		// bound each entry so a crafted archive can't decompress without limit
		if _, err := io.Copy(w, io.LimitReader(r, maxEntrySize)); err != nil {
			r.Close()
			return err
		}
		r.Close()
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: opfName, Method: zip.Deflate})
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(newOPF)); err != nil {
		return err
	}
	if coverZipPath != "" {
		cw, err := zw.CreateHeader(&zip.FileHeader{Name: coverZipPath, Method: zip.Store})
		if err != nil {
			return err
		}
		if _, err := cw.Write(coverBytes); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), fsPath)
}

var metadataCloseRe = regexp.MustCompile(`</(?:[A-Za-z0-9]+:)?metadata>`)

const coverFileName = "booky-cover.jpg"

// maxEntrySize caps how much we copy from any one archive entry when
// rewriting the OPF — a guard against decompression-bomb archives. Real
// ebook resources (cover art, fonts) are far under this.
const maxEntrySize = 200 << 20

var (
	coverMetaRe     = regexp.MustCompile(`<meta\s+name="cover"[^>]*/?>(?:</meta>)?`)
	coverItemRe     = regexp.MustCompile(`<item\s[^>]*id="booky-cover"[^>]*/?>(?:</item>)?`)
	manifestCloseRe = regexp.MustCompile(`</(?:[A-Za-z0-9]+:)?manifest>`)
)

var emptyManifestRe = regexp.MustCompile(`<manifest\s*/>`)

// addCoverToOPF points the standard cover meta + a manifest item at Booky's
// embedded image. Returns ok=false when the OPF has no manifest to hook into.
func addCoverToOPF(opf string) (string, bool) {
	opf = emptyManifestRe.ReplaceAllString(opf, "<manifest></manifest>")
	if manifestCloseRe.FindStringIndex(opf) == nil {
		return opf, false
	}
	opf = coverMetaRe.ReplaceAllString(opf, "")
	opf = coverItemRe.ReplaceAllString(opf, "")
	loc := metadataCloseRe.FindStringIndex(opf)
	if loc == nil {
		return opf, false
	}
	opf = opf[:loc[0]] + `<meta name="cover" content="booky-cover"/>` + "\n" + opf[loc[0]:]
	loc = manifestCloseRe.FindStringIndex(opf)
	item := `<item id="booky-cover" href="` + coverFileName + `" media-type="image/jpeg" properties="cover-image"/>` + "\n"
	return opf[:loc[0]] + item + opf[loc[0]:], true
}

// rewriteOPF removes the elements Booky owns (per enabled fields) and
// re-inserts fresh ones just before </metadata>.
func rewriteOPF(opf string, m Metadata, fields Fields) (string, error) {
	loc := metadataCloseRe.FindStringIndex(opf)
	if loc == nil {
		return "", fmt.Errorf("opf has no metadata element")
	}

	drop := func(patterns ...string) {
		for _, p := range patterns {
			opf = regexp.MustCompile(p).ReplaceAllString(opf, "")
		}
	}
	var add strings.Builder
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}

	if fields["title"] && m.Title != "" {
		drop(`(?s)<dc:title[^>]*>.*?</dc:title>`)
		fmt.Fprintf(&add, "<dc:title>%s</dc:title>\n", esc(m.Title))
	}
	if fields["author"] && len(m.Authors) > 0 {
		drop(`(?s)<dc:creator[^>]*>.*?</dc:creator>`)
		for _, a := range m.Authors {
			// xmlns:opf is declared inline: EPUB 3 packages usually don't
			// declare the prefix on <package>, and a bare opf:role makes the
			// whole OPF invalid XML for strict parsers (browsers, foliate)
			fmt.Fprintf(&add, `<dc:creator xmlns:opf="http://www.idpf.org/2007/opf" opf:role="aut">%s</dc:creator>`+"\n", esc(a))
		}
	}
	if fields["description"] && m.Description != "" {
		drop(`(?s)<dc:description[^>]*>.*?</dc:description>`)
		fmt.Fprintf(&add, "<dc:description>%s</dc:description>\n", esc(m.Description))
	}
	if fields["language"] && m.Language != "" {
		drop(`(?s)<dc:language[^>]*>.*?</dc:language>`)
		fmt.Fprintf(&add, "<dc:language>%s</dc:language>\n", esc(m.Language))
	}
	if fields["publisher"] && m.Publisher != "" {
		drop(`(?s)<dc:publisher[^>]*>.*?</dc:publisher>`)
		fmt.Fprintf(&add, "<dc:publisher>%s</dc:publisher>\n", esc(m.Publisher))
	}
	if fields["pubdate"] && m.Date != "" {
		drop(`(?s)<dc:date[^>]*>.*?</dc:date>`)
		fmt.Fprintf(&add, "<dc:date>%s</dc:date>\n", esc(m.Date))
	}
	if fields["series"] {
		drop(`<meta\s+name="calibre:series"[^>]*/?>(?:</meta>)?`,
			`<meta\s+name="calibre:series_index"[^>]*/?>(?:</meta>)?`)
		if m.SeriesName != "" {
			fmt.Fprintf(&add, `<meta name="calibre:series" content="%s"/>`+"\n", esc(m.SeriesName))
			if m.SeriesIndex > 0 {
				idx := strconv.FormatFloat(m.SeriesIndex, 'f', -1, 64)
				fmt.Fprintf(&add, `<meta name="calibre:series_index" content="%s"/>`+"\n", idx)
			}
		}
	}
	if fields["identifiers"] {
		// only identifiers with Booky's schemes are replaced — the package
		// unique-identifier (usually a uuid) is left alone
		drop(`(?si)<dc:identifier[^>]*scheme="(?:ISBN|GOODREADS|HARDCOVER)"[^>]*>.*?</dc:identifier>`)
		if m.ISBN != "" {
			fmt.Fprintf(&add, `<dc:identifier opf:scheme="ISBN">%s</dc:identifier>`+"\n", esc(m.ISBN))
		}
		if m.GoodreadsID != "" {
			fmt.Fprintf(&add, `<dc:identifier opf:scheme="GOODREADS">%s</dc:identifier>`+"\n", esc(m.GoodreadsID))
		}
		if m.HardcoverID != "" {
			fmt.Fprintf(&add, `<dc:identifier opf:scheme="HARDCOVER">%s</dc:identifier>`+"\n", esc(m.HardcoverID))
		}
	}

	// the drops changed offsets — find the close tag again on the cleaned doc
	loc = metadataCloseRe.FindStringIndex(opf)
	if loc == nil {
		return "", fmt.Errorf("opf metadata element lost during rewrite")
	}
	return opf[:loc[0]] + add.String() + opf[loc[0]:], nil
}
