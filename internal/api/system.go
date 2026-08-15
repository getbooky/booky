package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LogRing keeps the last maxLines of log output in memory so the Logs
// settings panel can show them without touching disk.
type LogRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func NewLogRing(max int) *LogRing {
	return &LogRing{max: max}
}

func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := 0
	for i, b := range p {
		if b == '\n' {
			if i > start {
				r.lines = append(r.lines, string(p[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(p) {
		r.lines = append(r.lines, string(p[start:]))
	}
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	return len(p), nil
}

func (r *LogRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	// the log stream carries every library's paths and provider chatter
	if !s.requireAdmin(w, r) {
		return
	}
	lines := []string{}
	if s.Logs != nil {
		lines = s.Logs.Lines()
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// handleBrowse suggests directories for path inputs (library roots, download
// folders): everything up to the last slash is the directory to list, the
// rest is a prefix filter. Directories only, capped, auth-guarded like the
// rest of the API.
//
// Suggestions never leave the media mount: everything these inputs may point
// at (library roots, the downloads folder, import sources) is fenced to it,
// so listing the rest of the host would only advertise paths the server will
// reject anyway. Above the mount the sole suggestion is the way in ("/" →
// "/data/"); anywhere else outside it, nothing.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	// this walks the server's filesystem to fill in path inputs — admin-only,
	// and the only remaining caller (library roots, downloads dir) is too
	if !s.requireAdmin(w, r) {
		return
	}
	raw := r.URL.Query().Get("path")
	if raw == "" {
		raw = s.mediaRoot + "/"
	}
	dir, partial := raw, ""
	if !strings.HasSuffix(raw, "/") {
		dir, partial = filepath.Dir(raw), strings.ToLower(filepath.Base(raw))
	}
	dir = filepath.Clean(dir)
	if !underRoot(dir, s.mediaRoot) {
		dirs := []string{}
		if underRoot(s.mediaRoot, dir) {
			// an ancestor of the mount: suggest the next component on the way
			// down to it, subject to the same prefix filter as a real listing
			rel, err := filepath.Rel(dir, s.mediaRoot)
			if err == nil {
				next := strings.SplitN(rel, string(filepath.Separator), 2)[0]
				if partial == "" || strings.HasPrefix(strings.ToLower(next), partial) {
					dirs = append(dirs, filepath.Join(dir, next)+"/")
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"dirs": dirs})
		return
	}
	// resolve symlinks before reading, so a link planted under the mount
	// can't serve listings of directories outside it
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || !underRoot(real, s.mediaRoot) {
		writeJSON(w, http.StatusOK, map[string]any{"dirs": []string{}})
		return
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"dirs": []string{}})
		return
	}
	dirs := []string{}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if partial != "" && !strings.HasPrefix(strings.ToLower(name), partial) {
			continue
		}
		dirs = append(dirs, filepath.Join(dir, name)+"/")
		if len(dirs) >= 25 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dirs": dirs})
}

// A health check is "ok", "error", or "pending" — pending marks integrations
// that exist in the product but aren't configured (or built) yet, so the
// panel shows the whole picture without false alarms.
type healthCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func check(name string, err error, okDetail string) healthCheck {
	if err != nil {
		return healthCheck{name, "error", err.Error()}
	}
	return healthCheck{name, "ok", okDetail}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// every library's name and root path, plus which integrations are
	// configured — the Health panel is an admin screen and so is its data
	if !s.requireAdmin(w, r) {
		return
	}
	checks := []healthCheck{check("Database", s.db.Ping(), "connected")}

	libs, err := s.Catalog.ListLibraries()
	if err != nil {
		checks = append(checks, healthCheck{"Libraries", "error", err.Error()})
	}
	for _, l := range libs {
		info, err := os.Stat(l.RootPath)
		switch {
		case err != nil:
			checks = append(checks, healthCheck{"Library: " + l.Name, "error", "root folder missing: " + l.RootPath})
		case !info.IsDir():
			checks = append(checks, healthCheck{"Library: " + l.Name, "error", l.RootPath + " is not a folder"})
		default:
			checks = append(checks, healthCheck{"Library: " + l.Name, "ok", l.RootPath})
		}
	}

	// integrations report pending until configured, live status once they are
	if s.Settings.Get("hardcover_token") == "" {
		checks = append(checks, healthCheck{"Hardcover", "pending", "no API token — Goodreads and Open Library still work"})
	} else {
		checks = append(checks, healthCheck{"Hardcover", "ok", "token configured"})
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if p := s.Acquire.Prowlarr(); p.Configured() {
		if st, err := p.Test(ctx); err != nil {
			checks = append(checks, healthCheck{"Indexers (Prowlarr)", "error", err.Error()})
		} else {
			checks = append(checks, healthCheck{"Indexers (Prowlarr)", "ok", "connected — Prowlarr " + st.Version})
		}
	} else {
		checks = append(checks, healthCheck{"Indexers (Prowlarr)", "pending", "not configured — set it up under Settings → Sources"})
	}
	if sab := s.Acquire.Sab(); sab.Configured() {
		if v, err := sab.Test(ctx); err != nil {
			checks = append(checks, healthCheck{"Download client (SABnzbd)", "error", err.Error()})
		} else {
			checks = append(checks, healthCheck{"Download client (SABnzbd)", "ok", "connected — SABnzbd " + v})
		}
	} else {
		checks = append(checks, healthCheck{"Download client (SABnzbd)", "pending", "not configured — set it up under Settings → Download clients"})
	}
	if z := s.Acquire.Zlib(); z.Configured() {
		if left, limit, err := z.Test(ctx); err != nil {
			checks = append(checks, healthCheck{"Z-Library", "error", err.Error()})
		} else {
			checks = append(checks, healthCheck{"Z-Library", "ok", fmt.Sprintf("connected — %d of %d downloads left today", left, limit)})
		}
	} else {
		checks = append(checks, healthCheck{"Z-Library", "pending", "not configured — add your account under Settings → Sources"})
	}
	if s.Acquire.Annas().HasMemberKey() {
		checks = append(checks, healthCheck{"Anna's Archive", "ok", "member key set — fast downloads enabled"})
	} else {
		checks = append(checks, healthCheck{"Anna's Archive", "pending", "no member key — free slow downloads only (add a key for reliability)"})
	}
	// watched lists: pending until one exists, then live poll status
	if lists, err := s.Watcher.Lists(); err != nil {
		checks = append(checks, healthCheck{"Watched lists", "error", err.Error()})
	} else if len(lists) == 0 {
		checks = append(checks, healthCheck{"Watched lists", "pending", "no lists yet — add one under Settings → Watched lists"})
	} else {
		failing := 0
		enabled := 0
		firstErr := ""
		for _, l := range lists {
			if l.Enabled {
				enabled++
			}
			if l.LastError != "" {
				failing++
				if firstErr == "" {
					firstErr = l.Name + ": " + l.LastError
				}
			}
		}
		if failing > 0 {
			checks = append(checks, healthCheck{"Watched lists", "error", firstErr})
		} else {
			checks = append(checks, healthCheck{"Watched lists", "ok",
				fmt.Sprintf("%d list(s) watched, %d enabled", len(lists), enabled)})
		}
	}

	ok := true
	for _, c := range checks {
		if c.Status == "error" {
			ok = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "checks": checks})
}
