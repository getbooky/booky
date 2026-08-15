package api

import (
	"net/http"
	"testing"
)

func TestQueueWantedHistoryProfiles(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	_, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/x"})
	libID := int64(out["id"].(float64))
	_, _ = doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "manual", "title": "Burrow", "authors": []string{"Emmett Hale"}},
		"libraryId": libID, "monitored": true,
	})

	rec, wanted := doJSON(t, h, "GET", "/api/v1/wanted", nil)
	if rec.Code != http.StatusOK || len(wanted["books"].([]any)) != 1 {
		t.Fatalf("wanted: %d %v", rec.Code, wanted)
	}

	rec, q := doJSON(t, h, "GET", "/api/v1/queue", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("queue: %d %v", rec.Code, q)
	}

	rec, hist := doJSON(t, h, "GET", "/api/v1/history", nil)
	if rec.Code != http.StatusOK || len(hist["history"].([]any)) == 0 {
		t.Fatalf("history should have the add entry: %d %v", rec.Code, hist)
	}

	rec, profs := doJSON(t, h, "GET", "/api/v1/profiles", nil)
	if rec.Code != http.StatusOK || len(profs["profiles"].([]any)) != 1 {
		t.Fatalf("profiles: %d %v", rec.Code, profs)
	}
	profID := int64(profs["profiles"].([]any)[0].(map[string]any)["id"].(float64))

	rec, _ = doJSON(t, h, "PUT", "/api/v1/profiles/1", map[string]any{
		"name": "EPUB only", "formats": []string{"epub"}, "preferredTerms": "retail",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update profile %d: %d", profID, rec.Code)
	}
	_, profs = doJSON(t, h, "GET", "/api/v1/profiles", nil)
	p := profs["profiles"].([]any)[0].(map[string]any)
	if p["name"] != "EPUB only" || p["cutoffFormat"] != "epub" {
		t.Errorf("profile after update = %v", p)
	}
}

func TestSecretSettingsMasked(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	for _, key := range []string{"prowlarr_api_key", "sab_api_key"} {
		rec, _ := doJSON(t, h, "PUT", "/api/v1/settings/"+key, map[string]string{"value": "super-secret"})
		if rec.Code != http.StatusOK {
			t.Fatalf("put %s: %d", key, rec.Code)
		}
		_, got := doJSON(t, h, "GET", "/api/v1/settings/"+key, nil)
		if got["value"] != "********" {
			t.Errorf("%s leaked: %v", key, got["value"])
		}
	}
}
