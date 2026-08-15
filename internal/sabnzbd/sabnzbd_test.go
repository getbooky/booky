package sabnzbd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("apikey") != "good-key" {
			_, _ = w.Write([]byte(`{"status":false,"error":"API Key Incorrect"}`))
			return
		}
		switch q.Get("mode") {
		case "version":
			_, _ = w.Write([]byte(`{"version":"4.3.2"}`))
		case "addurl":
			if q.Get("cat") != "booky" {
				t.Errorf("category = %q, want booky", q.Get("cat"))
			}
			_, _ = w.Write([]byte(`{"status":true,"nzo_ids":["SABnzbd_nzo_abc123"]}`))
		case "queue":
			_, _ = w.Write([]byte(`{"queue":{"slots":[
				{"nzo_id":"SABnzbd_nzo_abc123","filename":"First Ember","status":"Downloading","percentage":"42","sizeleft":"700 KB"}]}}`))
		case "history":
			if q.Get("name") == "delete" {
				if q.Get("value") != "SABnzbd_nzo_abc123" || q.Get("del_files") != "1" {
					t.Errorf("delete params = %v, want nzo id + del_files=1", q)
				}
				_, _ = w.Write([]byte(`{"status":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"history":{"slots":[
				{"nzo_id":"SABnzbd_nzo_abc123","name":"First Ember","status":"Completed","storage":"/data/downloads/booky/First Ember"}]}}`))
		default:
			t.Errorf("unexpected mode %q", q.Get("mode"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVersionAndAuth(t *testing.T) {
	srv := testServer(t)
	c := New(srv.URL, "good-key", "")
	v, err := c.Test(context.Background())
	if err != nil || v != "4.3.2" {
		t.Fatalf("Test: %v %q", err, v)
	}
	bad := New(srv.URL, "nope", "")
	if _, err := bad.Test(context.Background()); err == nil {
		t.Fatal("bad key should error")
	}
}

func TestAddURLQueueHistory(t *testing.T) {
	srv := testServer(t)
	c := New(srv.URL, "good-key", "booky")

	id, err := c.AddURL(context.Background(), "http://indexer/nzb/1", "First Ember")
	if err != nil || id != "SABnzbd_nzo_abc123" {
		t.Fatalf("AddURL: %v %q", err, id)
	}

	queue, err := c.Queue(context.Background())
	if err != nil || len(queue) != 1 || queue[0].Status != "Downloading" {
		t.Fatalf("Queue: %v %+v", err, queue)
	}

	hist, err := c.History(context.Background())
	if err != nil || len(hist) != 1 || hist[0].Storage == "" {
		t.Fatalf("History: %v %+v", err, hist)
	}

	// post-import cleanup: history entry and leftover files both removed
	if err := c.Delete(context.Background(), "SABnzbd_nzo_abc123", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
