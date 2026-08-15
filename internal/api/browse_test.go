package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func browseDirs(t *testing.T, h http.Handler, path string) []string {
	t.Helper()
	rec, out := doJSON(t, h, "GET", "/api/v1/system/browse?path="+path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("browse %q: %d %s", path, rec.Code, rec.Body)
	}
	raw, ok := out["dirs"].([]any)
	if !ok {
		t.Fatalf("browse %q: no dirs in %v", path, out)
	}
	dirs := make([]string, len(raw))
	for i, d := range raw {
		dirs[i] = d.(string)
	}
	return dirs
}

// The browse endpoint fills path inputs whose values are all fenced to the
// media mount, so its listings stay inside the mount too: subdirectories
// within it are suggested, anything outside returns nothing.
func TestBrowseStaysInsideMediaMount(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	mount := t.TempDir()
	srv.mediaRoot = mount
	for _, d := range []string{"books", "boxsets", "downloads"} {
		if err := os.Mkdir(filepath.Join(mount, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	dirs := browseDirs(t, h, mount+"/")
	if len(dirs) != 3 {
		t.Fatalf("mount listing = %v", dirs)
	}
	if dirs = browseDirs(t, h, mount+"/bo"); len(dirs) != 2 {
		t.Fatalf("prefix filter = %v", dirs)
	}

	// outside the mount: nothing
	for _, p := range []string{"/etc/", "/etc/cron"} {
		if dirs := browseDirs(t, h, p); len(dirs) != 0 {
			t.Errorf("browse %q leaked %v", p, dirs)
		}
	}
	// dot-dot climbs land on an ancestor, which suggests only the way back in
	for _, p := range []string{mount + "/../", mount + "/books/../../"} {
		if dirs := browseDirs(t, h, p); len(dirs) != 1 || dirs[0] != mount+"/" {
			t.Errorf("browse %q = %v, want [%s/]", p, dirs, mount)
		}
	}
}

// A symlink planted inside the mount must not serve listings of directories
// outside it.
func TestBrowseSymlinkEscapeListsNothing(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	mount := t.TempDir()
	srv.mediaRoot = mount
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mount, "escape")); err != nil {
		t.Fatal(err)
	}
	if dirs := browseDirs(t, h, mount+"/escape/"); len(dirs) != 0 {
		t.Errorf("symlink escape listed %v", dirs)
	}
}

// Above the mount the endpoint doesn't list the host's directories — the only
// suggestion is the way down into the mount, so typing "/" still leads
// somewhere useful.
func TestBrowseAboveMountSuggestsOnlyWayIn(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	// default mount: "/" and the partial "/d" suggest /data/, nothing else
	for _, p := range []string{"/", "/d", "/data"} {
		dirs := browseDirs(t, h, p)
		if len(dirs) != 1 || dirs[0] != "/data/" {
			t.Errorf("browse %q = %v, want [/data/]", p, dirs)
		}
	}
	if dirs := browseDirs(t, h, "/e"); len(dirs) != 0 {
		t.Errorf("browse /e = %v, want none", dirs)
	}

	// same shape when the mount sits deeper: an ancestor dir never lists its
	// real entries, only the next component toward the mount
	base := t.TempDir()
	mount := filepath.Join(base, "media")
	if err := os.MkdirAll(filepath.Join(mount, "books"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.mediaRoot = mount
	dirs := browseDirs(t, h, base+"/")
	if len(dirs) != 1 || dirs[0] != mount+"/" {
		t.Errorf("ancestor listing = %v, want [%s/]", dirs, mount)
	}
}

// Library roots are fenced at creation: the server rejects anything outside
// the media mount rather than trusting the UI's autocomplete to behave.
func TestCreateLibraryRootFencedToMediaMount(t *testing.T) {
	h := testServer(t).Handler()

	for _, root := range []string{"/etc/books", "/data/../etc", "relative/books", "/"} {
		rec, _ := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Bad", "rootPath": root})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("rootPath %q: %d, want 400 (%s)", root, rec.Code, rec.Body)
		}
	}

	rec, _ := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Good", "rootPath": "/data/books"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("rootPath under /data: %d %s", rec.Code, rec.Body)
	}
}

// The downloads folder setting gets the same fence: custom locations must sit
// under the media mount; empty falls back to the built-in default.
func TestDownloadsDirSettingFencedToMediaMount(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	for _, dir := range []string{"/etc", "/config/sneaky", "/data/../config", "downloads"} {
		rec, _ := doJSON(t, h, "PUT", "/api/v1/settings/downloads_dir", map[string]string{"value": dir})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("downloads_dir %q: %d, want 400 (%s)", dir, rec.Code, rec.Body)
		}
	}
	if got := srv.Settings.Get("downloads_dir"); got != "" {
		t.Errorf("rejected value was stored: %q", got)
	}

	rec, _ := doJSON(t, h, "PUT", "/api/v1/settings/downloads_dir", map[string]string{"value": "/data/downloads/custom"})
	if rec.Code != http.StatusOK {
		t.Fatalf("downloads_dir under /data: %d %s", rec.Code, rec.Body)
	}
	if got := srv.Settings.Get("downloads_dir"); got != "/data/downloads/custom" {
		t.Errorf("stored = %q", got)
	}

	// clearing it restores the default
	rec, _ = doJSON(t, h, "PUT", "/api/v1/settings/downloads_dir", map[string]string{"value": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear downloads_dir: %d %s", rec.Code, rec.Body)
	}
}
