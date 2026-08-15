package catalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CoverCache fetches each book's cover exactly once and serves it from disk
// forever after — browsing the library never triggers external requests.
type CoverCache struct {
	Dir    string
	Client *http.Client
}

func NewCoverCache(dir string) *CoverCache {
	return &CoverCache{Dir: dir, Client: &http.Client{Timeout: 20 * time.Second}}
}

func (c *CoverCache) path(bookID int64) string {
	return filepath.Join(c.Dir, fmt.Sprintf("%d.jpg", bookID))
}

// Path returns the cached cover file for bookID, or "" if none is cached.
func (c *CoverCache) Path(bookID int64) string {
	p := c.path(bookID)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// SaveBytes caches raw image bytes as the book's cover (used for covers
// extracted from the book file itself rather than downloaded).
func (c *CoverCache) SaveBytes(bookID int64, data []byte) error {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	tmp := c.path(bookID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path(bookID))
}

// Remove deletes the cached cover for bookID. The path is built internally
// from the numeric id, so no caller-supplied string reaches the filesystem.
// A missing file is not an error.
func (c *CoverCache) Remove(bookID int64) error {
	if err := os.Remove(c.path(bookID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ForceURL replaces the cached cover with the image at srcURL — the
// user-supplied-cover path, where overwriting is the point.
func (c *CoverCache) ForceURL(ctx context.Context, bookID int64, srcURL string) error {
	return c.Replace(ctx, bookID, srcURL)
}

// Ensure downloads the cover from srcURL unless already cached.
func (c *CoverCache) Ensure(ctx context.Context, bookID int64, srcURL string) error {
	if c.Path(bookID) != "" {
		return nil
	}
	return c.Replace(ctx, bookID, srcURL)
}

// Replace downloads the cover from srcURL and swaps it in even when one is
// already cached — the provider changed its art, or the user picked a new
// one. The download lands in a temp file and is renamed over the old cover
// only after it arrives intact, so a failed fetch leaves the existing cover
// untouched rather than deleting it first and hoping.
func (c *CoverCache) Replace(ctx context.Context, bookID int64, srcURL string) error {
	if srcURL == "" {
		return nil
	}
	u, err := url.Parse(srcURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("cover url rejected: %q", srcURL)
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("cover fetch status %d", res.StatusCode)
	}

	tmp := c.path(bookID) + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	// covers are small; 10MB cap guards against a hostile or broken source
	if _, err := io.Copy(f, io.LimitReader(res.Body, 10<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, c.path(bookID))
}

// SweepOrphanedCovers deletes cached cover and author-photo files whose row
// no longer exists. Deletion paths drop the file with the row, but files from
// before that behavior (or from a crash between the two steps) linger — and
// because row ids are reused, a lingering file would resurface as some future
// book's cover. Run once at startup.
func (s *Store) SweepOrphanedCovers() (int, error) {
	n, err := s.sweepCacheDir(s.Covers, `SELECT id FROM books`)
	if err != nil {
		return n, err
	}
	m, err := s.sweepCacheDir(s.AuthorPhotos, `SELECT id FROM authors`)
	return n + m, err
}

func (s *Store) sweepCacheDir(c *CoverCache, idQuery string) (int, error) {
	if c == nil {
		return 0, nil
	}
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	valid := map[int64]bool{}
	rows, err := s.db.Query(idQuery)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		valid[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue // the authors/ photo cache nests inside the covers dir
		}
		idStr, ok := strings.CutSuffix(e.Name(), ".jpg")
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || valid[id] {
			continue
		}
		if err := os.Remove(filepath.Join(c.Dir, e.Name())); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
