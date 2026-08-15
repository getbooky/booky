package api

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Browser uploads are untrusted input: the extension must be on the book
// allowlist AND the file's leading bytes must carry that format's real
// signature, so a renamed executable never enters the pipeline. This runs
// server-side — the file picker's accept= filter is a convenience, not a
// control.

var uploadBookExts = map[string]bool{
	".epub": true, ".azw": true, ".azw3": true, ".mobi": true,
	".pdf": true, ".cbz": true, ".cbr": true, ".fb2": true,
}

// resolveImportSource fences the "import a file already on the server" path.
//
// The endpoint copies a named path into a library and the result is then
// downloadable, which makes an unfenced version a read-any-file-on-disk
// primitive — /config/booky.db onto a shelf, then straight back out over
// HTTP. Being an admin is authority to configure Booky, not a reason to hand
// it an arbitrary path, so the fence applies to every caller: the source must
// resolve inside the media mount, the download staging dir, or a library root
// the caller may reach.
//
// Symlinks are resolved on both sides before comparing, so a link planted
// inside a library folder can't step out of it.
func (s *Server) resolveImportSource(a access, src string) (string, error) {
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return "", fmt.Errorf("can't read %s: %w", src, err)
	}
	// the config dir is never importable, even when a deployment nests it
	// inside the media mount — that's where booky.db lives
	if s.Backups != nil && underRoot(real, s.Backups.ConfigDir()) {
		return "", errors.New("that path is inside Booky's config directory")
	}
	roots, err := s.importRoots(a)
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		if underRoot(real, root) {
			return real, nil
		}
	}
	return "", fmt.Errorf("manual import reads only from %s, the downloads folder, or a library root — %s is outside all of them", s.mediaRoot, src)
}

// importRoots is what a manual import may read from: the download staging dir
// and the roots of the libraries the caller may reach, plus the whole media
// mount for admins. Library roots stay in the list for libraries created
// before roots were fenced to the media mount, which can be anywhere.
func (s *Server) importRoots(a access) ([]string, error) {
	dir := strings.TrimSpace(s.Settings.Get("downloads_dir"))
	if dir == "" {
		dir = "/data/downloads/booky"
	}
	roots := []string{dir}
	if a.admin() {
		roots = append(roots, s.mediaRoot)
	}
	libs, err := s.Catalog.ListLibraries()
	if err != nil {
		return nil, err
	}
	for _, l := range libs {
		if l.RootPath != "" && a.mayLibrary(l.ID) {
			roots = append(roots, l.RootPath)
		}
	}
	return roots, nil
}

// fenceMediaPath validates a user-supplied directory path that Booky will
// later read from or write to (a library root, the downloads folder): it must
// be absolute and sit inside the media mount. The path may not exist yet —
// that's fine for a new library, the check is lexical then — but when it
// does, its symlink-resolved location is what's checked, so a planted link
// can't smuggle the fence somewhere else. Returns the cleaned path to store.
func (s *Server) fenceMediaPath(raw string) (string, error) {
	p := filepath.Clean(strings.TrimSpace(raw))
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("must be a folder under %s", s.mediaRoot)
	}
	check := p
	if real, err := filepath.EvalSymlinks(p); err == nil {
		check = real
	}
	if !underRoot(check, s.mediaRoot) {
		return "", fmt.Errorf("must be a folder under %s", s.mediaRoot)
	}
	return p, nil
}

// underRoot reports whether an already-resolved path sits inside root. Rel
// does the work: anything outside comes back starting with "..", and a plain
// "." means the path IS the root.
func underRoot(path, root string) bool {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = filepath.Clean(root)
	}
	rel, err := filepath.Rel(realRoot, path)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// verifyBookUpload checks the written upload against its claimed extension.
// Callers delete the file when an error comes back.
func verifyBookUpload(path, ext string) error {
	if !uploadBookExts[ext] {
		return fmt.Errorf("file type %q is not an accepted book format", ext)
	}
	f, err := os.Open(path) //nolint:gosec // G304: path was built by receiveBookUpload from the downloads dir
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 128)
	n, _ := f.Read(head)
	head = head[:n]

	ok := false
	switch ext {
	case ".epub", ".cbz":
		// both are ZIP containers
		ok = bytes.HasPrefix(head, []byte("PK\x03\x04"))
	case ".pdf":
		ok = bytes.HasPrefix(head, []byte("%PDF"))
	case ".mobi", ".azw", ".azw3":
		// PalmDB: the type/creator signature lives at offset 60; Topaz-era
		// .azw files instead start with "TPZ"
		ok = (len(head) >= 68 && (bytes.Equal(head[60:68], []byte("BOOKMOBI")) || bytes.Equal(head[60:68], []byte("TEXtREAd")))) ||
			bytes.HasPrefix(head, []byte("TPZ"))
	case ".cbr":
		ok = bytes.HasPrefix(head, []byte("Rar!\x1a\x07"))
	case ".fb2":
		trimmed := bytes.TrimLeft(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), " \t\r\n")
		ok = bytes.HasPrefix(trimmed, []byte("<?xml")) || bytes.HasPrefix(trimmed, []byte("<FictionBook"))
	}
	if !ok {
		return fmt.Errorf("the file's content doesn't look like %s — upload rejected", strings.TrimPrefix(ext, "."))
	}
	return nil
}

// verifyImageBytes gates the custom-cover upload the same way: only real
// image data (JPEG, PNG, GIF, WebP) is cached as a cover.
func verifyImageBytes(data []byte) error {
	switch {
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")): // JPEG
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")): // PNG
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
	default:
		return fmt.Errorf("that upload isn't a JPEG, PNG, GIF, or WebP image")
	}
	return nil
}
