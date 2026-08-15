// Package zlibrary is an authenticated Z-Library provider: it logs in with
// the user's own account (a free account works, within its daily quota),
// searches the catalog, and downloads files — the same eapi surface the
// Z-Library KoReader plugin uses. No scraping, no captchas: being logged in
// is the whole trick.
package zlibrary

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/getbooky/booky/internal/release"
)

type Client struct {
	domains  func() []string // e.g. https://z-library.sk; first working wins
	email    string
	password string
	http     *http.Client

	mu     sync.Mutex
	userID string
	key    string // remix_userkey from login
	base   string // domain the session was established on
}

func New(domains func() []string, email, password string) *Client {
	return &Client{
		domains:  domains,
		email:    email,
		password: password,
		http:     &http.Client{Timeout: 45 * time.Second},
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.email != "" && c.password != ""
}

// login establishes (or reuses) a session. Z-Library sessions are a
// userid + userkey pair sent as cookies on every later call.
func (c *Client) login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != "" {
		return nil
	}
	var lastErr = fmt.Errorf("no z-library domains configured")
	for _, base := range c.domains() {
		form := url.Values{"email": {c.email}, "password": {c.password}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			base+"/eapi/user/login", strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var out struct {
			User struct {
				ID       json.Number `json:"id"`
				RemixKey string      `json:"remix_userkey"`
			} `json:"user"`
			Error string `json:"error"`
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out)
		res.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("z-library: bad login response from %s", base)
			continue
		}
		if out.Error != "" || out.User.RemixKey == "" {
			if out.Error == "" {
				out.Error = "login rejected"
			}
			// a rejected password won't succeed on another mirror — stop
			return fmt.Errorf("z-library: %s", out.Error)
		}
		c.userID, c.key, c.base = out.User.ID.String(), out.User.RemixKey, base
		return nil
	}
	return lastErr
}

func (c *Client) session() (base, userID, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base, c.userID, c.key
}

// resetSession drops the cached login so the next call re-authenticates —
// used when the server says the session expired.
func (c *Client) resetSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.key, c.userID, c.base = "", "", ""
}

func (c *Client) authed(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	base, userID, key := c.session()
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, err
	}
	// Auth is a pair of session cookies. We set the request header directly
	// rather than http.Cookie structs: Secure/HttpOnly/SameSite are
	// Set-Cookie response attributes with no meaning on an outbound request.
	req.Header.Set("Cookie", "remix_userid="+userID+"; remix_userkey="+key)
	return req, nil
}

// Test logs in and returns the daily-downloads state so the settings panel
// can show "connected, 7/10 left today".
func (c *Client) Test(ctx context.Context) (left, limit int, err error) {
	if err := c.login(ctx); err != nil {
		return 0, 0, err
	}
	req, err := c.authed(ctx, http.MethodGet, "/eapi/user/profile", nil)
	if err != nil {
		return 0, 0, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer res.Body.Close()
	var out struct {
		User struct {
			DownloadsToday json.Number `json:"downloads_today"`
			DownloadsLimit json.Number `json:"downloads_limit"`
		} `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return 0, 0, fmt.Errorf("z-library: bad profile response")
	}
	today, _ := out.User.DownloadsToday.Int64()
	lim, _ := out.User.DownloadsLimit.Int64()
	return int(lim - today), int(lim), nil
}

type zlibBook struct {
	ID        json.Number `json:"id"`
	Hash      string      `json:"hash"`
	Title     string      `json:"title"`
	Author    string      `json:"author"`
	Extension string      `json:"extension"`
	// filesizeString is a human-readable string ("1.85 MB") — NOT a number.
	// The numeric byte count lives in filesize (Size). Decoding this into a
	// json.Number fails the whole response, so it must stay a string.
	FilesizeStr string      `json:"filesizeString,omitempty"`
	Size        json.Number `json:"filesize"`
	Language    string      `json:"language"`
	Year        json.Number `json:"year"`
}

// Search queries the catalog. Download tokens are "zlib:<id>/<hash>" and are
// resolved at grab time so search never spends quota.
func (c *Client) Search(ctx context.Context, query string) ([]release.Release, error) {
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	form := url.Values{"message": {query}, "limit": {"20"}, "order": {"popular"}}
	req, err := c.authed(ctx, http.MethodPost, "/eapi/book/search", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Books []zlibBook `json:"books"`
		Error string     `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("z-library: bad search response")
	}
	if out.Error != "" {
		return nil, fmt.Errorf("z-library: %s", out.Error)
	}
	rels := make([]release.Release, 0, len(out.Books))
	for _, b := range out.Books {
		if b.Hash == "" {
			continue
		}
		title := strings.TrimSpace(b.Title)
		if b.Author != "" {
			title += " — " + strings.TrimSpace(b.Author)
		}
		if b.Extension != "" {
			title += " ." + strings.ToLower(b.Extension)
		}
		size, _ := b.Size.Int64()
		rels = append(rels, release.Release{
			Title:       title,
			Source:      "zlibrary",
			Protocol:    "direct",
			Format:      strings.ToLower(b.Extension),
			SizeBytes:   size,
			Language:    b.Language,
			DownloadURL: "zlib:" + b.ID.String() + "/" + b.Hash,
		})
	}
	return rels, nil
}

// Download resolves a "zlib:<id>/<hash>" token into the real file link and
// fetches it into dir. This is the call that spends one of the account's
// daily downloads. wantFormat (the release's known format, e.g. "epub") backs
// up the filename's extension when the download link carries none.
func (c *Client) Download(ctx context.Context, token, dir, wantFormat string) (string, error) {
	idHash, ok := strings.CutPrefix(token, "zlib:")
	if !ok {
		return "", fmt.Errorf("z-library: not a zlib token: %s", token)
	}
	if err := c.login(ctx); err != nil {
		return "", err
	}
	req, err := c.authed(ctx, http.MethodGet, "/eapi/book/"+idHash+"/file", nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		File struct {
			DownloadLink string `json:"downloadLink"`
			Description  string `json:"description"`
		} `json:"file"`
		Error string `json:"error"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out)
	res.Body.Close()
	if err != nil {
		return "", fmt.Errorf("z-library: bad file response")
	}
	if out.Error != "" {
		if strings.Contains(strings.ToLower(out.Error), "session") {
			c.resetSession()
		}
		return "", fmt.Errorf("z-library: %s", out.Error) // includes daily-limit errors
	}
	if out.File.DownloadLink == "" {
		return "", fmt.Errorf("z-library: no download link returned")
	}
	return c.fetchFile(ctx, out.File.DownloadLink, dir, wantFormat)
}

func (c *Client) fetchFile(ctx context.Context, fileURL, dir, wantFormat string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("z-library: file fetch returned %d", res.StatusCode)
	}
	// The download link is a redirect endpoint (".../redirection"), so the URL
	// base carries no real name — SaveDownload prefers the Content-Disposition
	// filename and keeps two concurrent downloads off the same one.
	path, err := release.SaveDownload(res, fileURL, dir, wantFormat, "zlibrary-download")
	if err != nil {
		return "", fmt.Errorf("z-library: %w", err)
	}
	return path, nil
}
