// Package prowlarr talks to a Prowlarr instance: indexer sync and release
// search. Booky never talks to indexers directly — Prowlarr owns them.
package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/release"
)

type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

// New returns a client for the Prowlarr at baseURL (e.g. http://prowlarr:9696).
func New(baseURL, apiKey string) *Client {
	return &Client{
		base:   strings.TrimRight(baseURL, "/"),
		apiKey: apiKey,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Configured() bool { return c != nil && c.base != "" && c.apiKey != "" }

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("prowlarr: %w", err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("prowlarr: API key rejected")
	default:
		return fmt.Errorf("prowlarr: %s returned %d", path, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

type SystemStatus struct {
	Version string `json:"version"`
}

// Test verifies the URL + API key by asking for Prowlarr's version.
func (c *Client) Test(ctx context.Context) (SystemStatus, error) {
	var st SystemStatus
	err := c.get(ctx, "/api/v1/system/status", nil, &st)
	return st, err
}

type Indexer struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Enable   bool   `json:"enable"`
	Protocol string `json:"protocol"` // usenet | torrent
	Priority int    `json:"priority"`
}

func (c *Client) Indexers(ctx context.Context) ([]Indexer, error) {
	var out []Indexer
	err := c.get(ctx, "/api/v1/indexer", nil, &out)
	return out, err
}

// searchResult is Prowlarr's release shape (subset).
type searchResult struct {
	Title       string `json:"title"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"downloadUrl"`
	InfoURL     string `json:"infoUrl"`
	Indexer     string `json:"indexer"`
	Protocol    string `json:"protocol"`
}

// Search runs a free-text query across enabled indexers in the books
// categories (7000 covers ebooks on both usenet and torrent trackers).
func (c *Client) Search(ctx context.Context, query string) ([]release.Release, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Add("categories", "7000")
	q.Set("type", "search")
	var results []searchResult
	if err := c.get(ctx, "/api/v1/search", q, &results); err != nil {
		return nil, err
	}
	out := make([]release.Release, 0, len(results))
	for _, r := range results {
		out = append(out, release.Release{
			Title:       r.Title,
			Source:      "prowlarr:" + r.Indexer,
			Protocol:    r.Protocol,
			SizeBytes:   r.Size,
			DownloadURL: r.DownloadURL,
			InfoURL:     r.InfoURL,
			Indexer:     r.Indexer,
		})
	}
	return out, nil
}
