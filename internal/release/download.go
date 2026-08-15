package release

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxDownloadBytes caps a single book download. Generous for a book, small
// enough that a misresolved link can't fill the disk.
const maxDownloadBytes = 500 << 20

// SaveDownload streams an open book response into dir and returns the path it
// landed on.
//
// Two things a plain copy wouldn't do. The name: direct sources serve most
// books from a redirect endpoint whose URL path is a meaningless token
// (".../redirection"), so Content-Disposition and the release's known format
// are what actually name the file — and when all three are silent, the file's
// own leading bytes pick the extension, because the importer reads the format
// off that extension and calls anything without one an epub.
//
// And the landing: the name is reserved with O_EXCL before the bytes are
// moved onto it. Two grabs running at once are routinely handed the SAME
// name by the same source, and a plain rename would have the second silently
// replace the first — a download that was still sitting in the queue waiting
// to be imported, now gone, with the queue row pointing at someone else's
// book.
func SaveDownload(res *http.Response, fileURL, dir, wantFormat, fallback string) (string, error) {
	name := DownloadFilename(res, fileURL, wantFormat, fallback)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".booky-dl-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp) //nolint:errcheck // best-effort: gone already on the success path
	if _, err := io.Copy(f, io.LimitReader(res.Body, maxDownloadBytes)); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if filepath.Ext(name) == "" {
		name += SniffExt(tmp)
	}
	dst, err := reserveName(dir, name)
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(dst) // don't leave the empty reservation behind
		return "", err
	}
	return dst, nil
}

// reserveName creates the first free name in dir — "First Ember.epub", then
// "First Ember (2).epub" — and returns its path. Creating rather than merely
// checking is the point: an exists-then-write test would let two downloads
// that raced past the check both pick the same name.
func reserveName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 1; n <= 999; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", base, n, ext)
		}
		path := filepath.Join(dir, candidate)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644) //nolint:gosec // G304: dir is the configured downloads dir, name is sanitized by DownloadFilename
		if err == nil {
			f.Close()
			return path, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("no free filename for %q in %s", name, dir)
}

// SniffExt names a file from its own leading bytes, for the download that
// arrives with no extension from any source. Without it the importer's blind
// ".epub" default shelves a pdf as an epub — wrong format on the shelf, and
// a metadata write attempted on something that isn't a zip.
//
// Returns "" when nothing matches, leaving the old default in place: a guess
// we can't make is better left to the caller than made badly.
func SniffExt(path string) string {
	f, err := os.Open(path) //nolint:gosec // G304: caller's own temp file
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 132)
	n, _ := f.Read(head)
	head = head[:n]

	switch {
	case bytes.HasPrefix(head, []byte("%PDF")):
		return ".pdf"
	case bytes.HasPrefix(head, []byte("Rar!\x1a\x07")):
		return ".cbr"
	case bytes.HasPrefix(head, []byte("TPZ")):
		return ".azw"
	case len(head) >= 68 && (bytes.Equal(head[60:68], []byte("BOOKMOBI")) || bytes.Equal(head[60:68], []byte("TEXtREAd"))):
		return ".mobi"
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		// A zip could be an epub or a cbz, and the head alone can't always
		// tell (a conformant epub stores its mimetype first, but plenty
		// aren't conformant). Epub is the right guess in a book pipeline —
		// and it's what the importer would have assumed anyway.
		return ".epub"
	}
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), " \t\r\n")
	if bytes.HasPrefix(trimmed, []byte("<?xml")) || bytes.HasPrefix(trimmed, []byte("<FictionBook")) {
		return ".fb2"
	}
	return ""
}
