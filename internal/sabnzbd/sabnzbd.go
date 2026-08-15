// Package sabnzbd sends NZBs to a SABnzbd instance and reads back queue
// state so imports can fire the moment a download lands.
package sabnzbd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	base     string
	apiKey   string
	category string
	http     *http.Client
}

// New returns a client for the SABnzbd at baseURL (e.g. http://sabnzbd:8080).
// category is the SAB category downloads are filed under (default "booky").
func New(baseURL, apiKey, category string) *Client {
	if category == "" {
		category = "booky"
	}
	return &Client{
		base:     strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		category: category,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Configured() bool { return c != nil && c.base != "" && c.apiKey != "" }

func (c *Client) call(ctx context.Context, params url.Values, out any) error {
	params.Set("apikey", c.apiKey)
	params.Set("output", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sabnzbd: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("sabnzbd: returned %d", res.StatusCode)
	}
	// SAB reports auth errors inside a 200 body
	var probe struct {
		Status *bool  `json:"status"`
		Error  string `json:"error"`
	}
	raw := json.RawMessage{}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return fmt.Errorf("sabnzbd: bad response: %w", err)
	}
	if err := json.Unmarshal(raw, &probe); err == nil && probe.Error != "" {
		return fmt.Errorf("sabnzbd: %s", probe.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Test verifies URL + API key by asking for SAB's version.
func (c *Client) Test(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	q := url.Values{"mode": {"version"}}
	if err := c.call(ctx, q, &out); err != nil {
		return "", err
	}
	if out.Version == "" {
		return "", fmt.Errorf("sabnzbd: no version in response")
	}
	return out.Version, nil
}

// AddURL queues an NZB by URL under Booky's category and returns the SAB job
// id (nzo id) used to track it.
func (c *Client) AddURL(ctx context.Context, nzbURL, name string) (string, error) {
	q := url.Values{
		"mode": {"addurl"},
		"name": {nzbURL},
		"cat":  {c.category},
	}
	if name != "" {
		q.Set("nzbname", name)
	}
	var out struct {
		Status bool     `json:"status"`
		IDs    []string `json:"nzo_ids"`
	}
	if err := c.call(ctx, q, &out); err != nil {
		return "", err
	}
	if !out.Status || len(out.IDs) == 0 {
		return "", fmt.Errorf("sabnzbd: rejected nzb url")
	}
	return out.IDs[0], nil
}

// QueueItem is one active download.
type QueueItem struct {
	ID         string `json:"nzo_id"`
	Name       string `json:"filename"`
	Status     string `json:"status"`
	Percentage string `json:"percentage"`
	SizeLeft   string `json:"sizeleft"`
}

func (c *Client) Queue(ctx context.Context) ([]QueueItem, error) {
	var out struct {
		Queue struct {
			Slots []QueueItem `json:"slots"`
		} `json:"queue"`
	}
	if err := c.call(ctx, url.Values{"mode": {"queue"}}, &out); err != nil {
		return nil, err
	}
	return out.Queue.Slots, nil
}

// HistoryItem is one finished (or failed) download.
type HistoryItem struct {
	ID      string `json:"nzo_id"`
	Name    string `json:"name"`
	Status  string `json:"status"` // Completed | Failed
	Storage string `json:"storage"`
	Fail    string `json:"fail_message"`
}

func (c *Client) History(ctx context.Context) ([]HistoryItem, error) {
	var out struct {
		History struct {
			Slots []HistoryItem `json:"slots"`
		} `json:"history"`
	}
	q := url.Values{"mode": {"history"}, "category": {c.category}}
	if err := c.call(ctx, q, &out); err != nil {
		return nil, err
	}
	return out.History.Slots, nil
}

// Delete removes a job from SAB's history; delFiles also deletes whatever
// remains of its completed folder on disk. Called after a successful import
// so finished downloads don't pile up in SAB forever.
func (c *Client) Delete(ctx context.Context, nzoID string, delFiles bool) error {
	q := url.Values{
		"mode":  {"history"},
		"name":  {"delete"},
		"value": {nzoID},
	}
	if delFiles {
		q.Set("del_files", "1")
	}
	var out struct {
		Status bool `json:"status"`
	}
	if err := c.call(ctx, q, &out); err != nil {
		return err
	}
	if !out.Status {
		return fmt.Errorf("sabnzbd: delete of %s not acknowledged", nzoID)
	}
	return nil
}
