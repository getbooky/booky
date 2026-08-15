package koreader

import (
	"archive/zip"
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/getbooky/booky/internal/db"
)

// The plugin ships as Lua source generated from Go strings, and nothing else
// in the build ever runs it: a syntax error would sail through CI and only
// surface as a plugin that refuses to load on someone's e-reader. These tests
// compile every file the zip carries.

// mustCompile parses and compiles src the way KOReader's Lua would, without
// running it — main.lua's requires only exist inside KOReader.
func mustCompile(t *testing.T, name, src string) {
	t.Helper()
	if _, err := lua.NewState().LoadString(src); err != nil {
		t.Fatalf("%s does not compile: %v", name, err)
	}
}

func TestStaticPluginLuaCompiles(t *testing.T) {
	mustCompile(t, "_meta.lua", metaLua)
	mustCompile(t, "main.lua", mainLua)
}

// The zip is the actual artifact people install, so compile what it holds
// rather than the constants behind it — that covers any file added later.
func TestBuiltPluginZipIsValidLua(t *testing.T) {
	var buf bytes.Buffer
	d := &Device{ID: 1, Name: "Kobo Libra", Token: "tok-123", Libraries: []int64{1, 2}, AutoIDs: []int64{2}}
	if err := (&Store{}).BuildPlugin(&buf, d, "https://booky.example.com/"); err != nil {
		t.Fatalf("BuildPlugin: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	seen := 0
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".lua") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		src, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		mustCompile(t, f.Name, string(src))
		seen++
	}
	if seen != 3 {
		t.Errorf("compiled %d lua files, want the 3 the plugin ships", seen)
	}
}

// config.lua interpolates values the user chose — a device name, a server URL
// — into Lua source with Go's %q. Go and Lua agree on the escapes that matter,
// but that's a property worth pinning: a name carrying a quote, a backslash or
// a comment marker must come back out intact rather than ending the string
// early and mangling the file.
func TestConfigLuaSurvivesAwkwardDeviceNames(t *testing.T) {
	names := map[string]string{
		"double quote":    `Alex's "Kobo"`,
		"backslash":       `C:\books\kobo`,
		"lua comment":     "Kobo -- [[ not a comment",
		"long bracket":    "Kobo ]] .. os.exit() .. [[",
		"newline":         "Kobo\nLibra",
		"tab":             "Kobo\tLibra",
		"unicode":         "Kobo Libra – Alex's 📚",
		"trailing escape": `Kobo\`,
	}
	for label, name := range names {
		t.Run(label, func(t *testing.T) {
			d := &Device{ID: 1, Name: name, Token: "tok\"123\\", Libraries: []int64{7}, AutoIDs: nil}
			src := configLua(d, "https://booky.example.com")

			// config.lua is pure data (a `return {…}` table with no requires),
			// so it can be run — which checks the escaping, not just the syntax.
			L := lua.NewState()
			defer L.Close()
			if err := L.DoString(src); err != nil {
				t.Fatalf("config.lua for %q does not load: %v\n---\n%s", name, err, src)
			}
			tbl, ok := L.Get(-1).(*lua.LTable)
			if !ok {
				t.Fatalf("config.lua returned %s, want a table", L.Get(-1).Type())
			}
			if got := tbl.RawGetString("device_name").String(); got != name {
				t.Errorf("device_name = %q, want %q", got, name)
			}
			if got := tbl.RawGetString("token").String(); got != d.Token {
				t.Errorf("token = %q, want %q", got, d.Token)
			}
			if got := tbl.RawGetString("server_url").String(); got != "https://booky.example.com" {
				t.Errorf("server_url = %q", got)
			}
			// library ids travel as a JSON string main.lua decodes
			if got := tbl.RawGetString("libraries").String(); got != "[7]" {
				t.Errorf("libraries = %q, want [7]", got)
			}
		})
	}
}

// Auto-sync only ever runs from an event handler, so the set of handlers IS
// the feature. A refactor can drop one and every other test here still
// passes: the Lua compiles, the zip builds, and the plugin installs — it just
// silently stops syncing, which is exactly the failure this is guarding.
//
// The gap these cover: on a fresh install the startup check fires ten seconds
// in and gives up if wifi isn't up yet, and the network-connect check bails
// when no download folder has been chosen. Setting the folder afterwards left
// nothing to try again until the next reboot.
func TestAutoSyncKeepsAllItsTriggers(t *testing.T) {
	for _, want := range []struct{ decl, why string }{
		{"function Booky:onNetworkConnected()", "wifi coming up must check in"},
		{"function Booky:onResume()", "waking the device is when people expect new books"},
	} {
		if !strings.Contains(mainLua, want.decl) {
			t.Errorf("missing %s — %s", want.decl, want.why)
		}
	}

	// Choosing the folder from the menu has to kick a sync itself. It is
	// usually the last blocker on a fresh install, and nothing else retries.
	idx := strings.Index(mainLua, "function Booky:chooseDownloadDir(after)")
	if idx < 0 {
		t.Fatal("chooseDownloadDir is gone")
	}
	body := mainLua[idx : idx+900]
	if !strings.Contains(body, "self:autoSync()") {
		t.Error("setting the download folder no longer triggers a sync — a fresh install would wait for a reboot")
	}
	// ...but not while a manual download is already waiting on that folder,
	// or the two race for the same files.
	if !strings.Contains(body, "if after then") {
		t.Error("chooseDownloadDir must not auto-sync when a caller is already downloading")
	}
}

// Every handler must be safe to fire with no network. onResume runs on every
// wake, including deliberate offline reading, so the offline path has to stay
// silent rather than nag.
func TestAutoSyncBailsSilentlyWhenOffline(t *testing.T) {
	idx := strings.Index(mainLua, "function Booky:autoSync()")
	if idx < 0 {
		t.Fatal("autoSync is gone")
	}
	body := mainLua[idx : idx+400]
	first := strings.Index(body, "if not NetworkMgr:isOnline() then return end")
	if first < 0 {
		t.Fatal("autoSync must check the network before anything else")
	}
	// nothing user-facing may run before that early return
	for _, noisy := range []string{"InfoMessage", "UIManager:show", "ConfirmBox"} {
		if at := strings.Index(body, noisy); at >= 0 && at < first {
			t.Errorf("autoSync shows %s before its offline check — that would fire on every wake with no wifi", noisy)
		}
	}
}

// The devices page shows "last sync 21:14" in the reader's own timezone,
// which only works if the zone leaves the database attached to the stamp.
func TestDeviceTimesCarryTheirZone(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := New(conn)
	created, err := s.Create("Kobo", []int64{1}, []int64{1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.TouchSync(created.ID)
	d, err := s.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, got string }{
		{"createdAt", d.CreatedAt}, {"lastSync", d.LastSync},
	} {
		if !strings.HasSuffix(tc.got, "Z") || !strings.Contains(tc.got, "T") {
			t.Errorf("%s = %q, want RFC3339", tc.name, tc.got)
		}
	}
}
