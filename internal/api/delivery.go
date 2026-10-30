package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/koreader"
	"github.com/getbooky/booky/internal/metadata"
	"github.com/getbooky/booky/internal/watcher"
)

const sessionCookie = "booky_session"

// ---- auth middleware ----

// requireAuth wraps the API surface. OPDS and KoReader routes carry their own
// per-library / per-device credentials; the login endpoint and auth probe are
// open; everything else under /api needs a session once any account exists —
// before the first user is created (first run), the API is open for the wizard.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	open := map[string]bool{
		"/api/v1/auth/login":    true,
		"/api/v1/auth/me":       true,
		"/api/v1/system/status": true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, "/api/") || open[p] ||
			strings.HasPrefix(p, "/api/koreader/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.Auth.Enabled() {
			next.ServeHTTP(w, r)
			return
		}
		if s.sessionUser(r) == nil {
			writeErr(w, http.StatusUnauthorized, errors.New("login required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sessionUser(r *http.Request) *auth.User {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	return s.Auth.SessionUser(c.Value)
}

// ---- login / logout / me ----

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	token, user, err := s.Auth.Login(req.Username, req.Password, ip)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	// gosec G124 wants a literal Secure:true, but browsers refuse Secure
	// cookies set over plain HTTP — that would break the documented
	// unraid-style HTTP-on-LAN deployment outright. Secure is set whenever
	// the request actually arrived over TLS (directly or via proxy), and
	// HttpOnly + SameSite=Strict hold in both cases.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is request-dependent, see above
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
		MaxAge: int((30 * 24 * time.Hour).Seconds()),
	})
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: expiring the cookie; Secure matches how it was set
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
		MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// isHTTPS marks cookies Secure when the request arrived over TLS (directly
// or behind a reverse proxy). Unconditional Secure would silently break the
// common self-hosted plain-HTTP-on-LAN deployment.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// handleMe reports auth state: whether login is required at all, and who (if
// anyone) this session belongs to. The SPA decides between login screen and app.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"authRequired": s.Auth.Enabled()}
	if u := s.sessionUser(r); u != nil {
		resp["user"] = u
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- users ----

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	users, err := s.Auth.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		// TagIDs are the shared tags the account joins. Its personal tag is
		// created with it; an account with no shared tags sees only what it
		// adds for itself.
		TagIDs []int64 `json:"tagIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	// the very first account must be an admin, or nobody can manage anything
	if !s.Auth.Enabled() {
		req.Role = "admin"
	}
	id, err := s.Auth.CreateUser(req.Username, req.Password, req.Role, req.TagIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// handleSetUserTags changes which shared tags an account holds, so access can
// be granted or taken away without recreating the user. It takes effect on
// the account's next request and on its devices' next sync — tags are read
// per request, never baked into a session or a device.
func (s *Server) handleSetUserTags(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req struct {
		TagIDs []int64 `json:"tagIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if s.Auth.UserByID(id) == nil {
		writeErr(w, http.StatusNotFound, errors.New("user not found"))
		return
	}
	if err := s.Auth.SetUserTags(id, req.TagIDs); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Auth.UserByID(id))
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if err := s.Auth.DeleteUser(id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- OPDS ----

// handleOPDSStatus tells the signed-in account where its feed lives. A reader
// app signs in with the same username and password as the web UI.
func (s *Server) handleOPDSStatus(w http.ResponseWriter, r *http.Request) {
	u := s.sessionUser(r)
	if u == nil {
		writeErr(w, http.StatusForbidden, errors.New("sign in first"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": u.Username,
		"feedPath": "/opds",
	})
}

// ---- devices (KoReader) ----

// Pairing an e-reader is everyday business, not administration, so every
// account reaches the devices page — but only ever its own devices, and the
// libraries a device can carry are capped by what its owner may reach.

// ownsDevice reports whether the caller may see or manage a device. Admins
// manage every device including the unowned ones left over from before
// accounts existed; everyone else only what they paired themselves.
func (s *Server) ownsDevice(a access, d *koreader.Device) bool {
	if a.admin() {
		return true
	}
	return a.user != nil && d.OwnerID == a.user.ID
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.KoReader.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// An admin sees every device so they can revoke a user's e-reader when
	// they need to, labelled with who paired it. A user sees only their own,
	// so the label is noise and stays empty.
	a := s.access(r)
	owners := map[int64]string{}
	if a.admin() && s.Auth.Enabled() {
		users, err := s.Auth.ListUsers()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		for _, u := range users {
			owners[u.ID] = u.Username
		}
	}
	type deviceView struct {
		koreader.Device
		OwnerName string `json:"ownerName,omitempty"`
	}
	visible := []deviceView{}
	for i := range devices {
		if !s.ownsDevice(a, &devices[i]) {
			continue
		}
		view := deviceView{Device: devices[i]}
		if a.admin() {
			switch name, ok := owners[devices[i].OwnerID]; {
			case ok:
				view.OwnerName = name
			case devices[i].OwnerID == 0:
				view.OwnerName = "unassigned"
			default:
				// the account was deleted; deviceMayAccess already refuses to
				// serve it, but the row is still here to be revoked
				view.OwnerName = "deleted account"
			}
		}
		visible = append(visible, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": visible})
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string  `json:"name"`
		TagIDs  []int64 `json:"tagIds"`
		AutoIDs []int64 `json:"autoTagIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// the plugin zip is useless without the server URL baked in — require it
	// up front so a fresh device's zip always "just works"
	if strings.TrimSpace(s.Settings.Get("server_url")) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("set the Server URL first (KoReader devices page) — it's baked into the plugin zip so the device knows where to sync"))
		return
	}
	// A device's token bypasses session auth by design, so a user must not be
	// able to mint one that reaches past their own tags. Both lists are
	// checked; auto-download is a subset of the device's tags, but check it
	// anyway rather than depend on that invariant holding elsewhere.
	// Before the first account exists (the setup wizard) a device is paired
	// unowned, as it always was; only an admin can see or edit it later.
	a := s.access(r)
	var ownerID int64
	if a.user != nil {
		ownerID = a.user.ID
	} else if !a.open {
		writeErr(w, http.StatusForbidden, errors.New("sign in first — devices belong to an account"))
		return
	}
	if err := s.ownerHoldsTags(ownerID, append(append([]int64{}, req.TagIDs...), req.AutoIDs...)); err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	device, err := s.KoReader.Create(strings.TrimSpace(req.Name), req.TagIDs, req.AutoIDs, ownerID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, device)
}

// handleUpdateDevice edits a KoReader device in place: name, tags,
// auto-download flags. The token survives, so the plugin already on the
// device keeps syncing against the NEW lists immediately (sync responses are
// computed server-side) — only the zip's own baked lists go stale, fixed by
// re-downloading it.
func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	a := s.access(r)
	device, err := s.KoReader.Get(id)
	if err != nil || !s.ownsDevice(a, device) {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	var req struct {
		Name    string  `json:"name"`
		TagIDs  []int64 `json:"tagIds"`
		AutoIDs []int64 `json:"autoTagIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// same rule as pairing, capped by the device's OWNER: its token bypasses
	// session auth, so it must never reach past the account it belongs to
	if err := s.ownerHoldsTags(device.OwnerID, append(append([]int64{}, req.TagIDs...), req.AutoIDs...)); err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	updated, err := s.KoReader.Update(id, strings.TrimSpace(req.Name), req.TagIDs, req.AutoIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	device, err := s.KoReader.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	// someone else's device reads as absent rather than forbidden — its
	// existence isn't the caller's business
	if !s.ownsDevice(s.access(r), device) {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	if err := s.KoReader.Revoke(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	_ = s.Catalog.AddHistory(0, s.Catalog.TheLibrary(), "removed",
		fmt.Sprintf("device %s revoked — its token stops working immediately", quoteName(device.Name, id)))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handlePluginZip(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	device, err := s.KoReader.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	// the zip carries the device's bearer token — never hand it to anyone but
	// the account that paired it
	if !s.ownsDevice(s.access(r), device) {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	// rows carry only the token's hash — recover the raw one for the zip
	raw, err := s.KoReader.RawToken(device.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	device.Token = raw
	serverURL := s.Settings.Get("server_url")
	w.Header().Set("Content-Type", "application/zip")
	// the filename doubles as the folder name desktop extractors create, so
	// unzipping yields booky.koplugin ready to copy into KOReader's plugins
	// directory (the entries sit at the zip root — see BuildPlugin)
	w.Header().Set("Content-Disposition", `attachment; filename="booky.koplugin.zip"`)
	if err := s.KoReader.BuildPlugin(w, device, serverURL); err != nil {
		// headers are gone; at least log it
		log.Printf("api: build plugin for device %d: %v", device.ID, err)
	}
}

// ---- KoReader device API (bearer token, no session) ----

// deviceTags is what a device may reach RIGHT NOW: its own tags, narrowed to
// the ones its owner still holds. A device token skips session auth entirely,
// so without this a person whose tag was taken away would keep syncing those
// books from their e-reader forever. A device whose owner was deleted
// reaches nothing. Devices paired before accounts existed carry owner 0 and
// keep their own tags, as they always have — only an admin can see or edit
// them.
func (s *Server) deviceTags(d *koreader.Device) []int64 {
	if d.OwnerID == 0 || !s.Auth.Enabled() {
		return d.TagIDs
	}
	owner := s.Auth.UserByID(d.OwnerID)
	if owner == nil {
		return nil
	}
	out := []int64{}
	for _, t := range d.TagIDs {
		if owner.IsAdmin() || owner.HasTag(t) {
			out = append(out, t)
		}
	}
	return out
}

// deviceReach reports whether a device may fetch bookID through tagID: the
// device carries the tag, its owner holds it, and the book carries it.
func (s *Server) deviceReach(d *koreader.Device, tagID, bookID int64) bool {
	for _, t := range s.deviceTags(d) {
		if t == tagID {
			return s.Catalog.BookVisible(bookID, []int64{tagID})
		}
	}
	return false
}

func (s *Server) koreaderDevice(w http.ResponseWriter, r *http.Request) (deviceID int64, ok bool) {
	header := r.Header.Get("Authorization")
	token, found := strings.CutPrefix(header, "Bearer ")
	if !found {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErr(w, http.StatusUnauthorized, errors.New("device token required"))
		return 0, false
	}
	device, err := s.KoReader.ByToken(strings.TrimSpace(token))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unknown or revoked device"))
		return 0, false
	}
	return device.ID, true
}

// handleKoSync lists every on-shelf book in the device's libraries. The
// plugin keeps its own record of what it already downloaded, so the response
// is idempotent — no server-side cursor to corrupt.
func (s *Server) handleKoSync(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := s.koreaderDevice(w, r)
	if !ok {
		return
	}
	device, err := s.KoReader.Get(deviceID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unknown device"))
		return
	}
	// series/genres ride along so the plugin's browse view can offer
	// author/series/genre filtering without extra endpoints
	type syncBook struct {
		ID         int64    `json:"id"`
		LibraryID  int64    `json:"libraryId"`
		Title      string   `json:"title"`
		Author     string   `json:"author"`
		SeriesName string   `json:"seriesName,omitempty"`
		SeriesNum  float64  `json:"seriesNum,omitempty"`
		Genres     []string `json:"genres,omitempty"`
		Filename   string   `json:"filename"`
		Format     string   `json:"format"`
		SizeBytes  int64    `json:"sizeBytes"`
	}
	// The plugin keys what it has downloaded by "book:libraryId" and fetches
	// /download/{libraryId}/{book}. Libraries became tags of the same id, so
	// that field now carries the tag the device reaches the book through —
	// a plugin already on a device sees exactly the keys it stored before
	// and never re-downloads a book it has. An auto-download tag wins, so the
	// plugin's auto[] lookup still fires for it.
	tags := s.deviceTags(device)
	books := []syncBook{}
	if len(tags) > 0 {
		list, err := s.Catalog.ListBooks(0, 0, 0)
		if err == nil {
			for _, b := range list {
				if b.FilePath == "" {
					continue
				}
				via := int64(0)
				for _, t := range tags {
					if !containsID(b.Tags, t) {
						continue
					}
					if via == 0 || (device.AutoFor(t) && !device.AutoFor(via)) {
						via = t
					}
				}
				if via == 0 {
					continue
				}
				books = append(books, syncBook{
					ID: b.ID, LibraryID: via, Title: b.Title, Author: b.Author,
					SeriesName: b.SeriesName, SeriesNum: b.SeriesNum, Genres: b.Genres,
					Filename: filepath.Base(b.FilePath), Format: b.FileFormat, SizeBytes: b.FileSize,
				})
			}
		}
	}
	s.KoReader.TouchSync(device.ID)
	writeJSON(w, http.StatusOK, map[string]any{"device": device.Name, "books": books})
}

func (s *Server) handleKoDownload(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := s.koreaderDevice(w, r)
	if !ok {
		return
	}
	device, err := s.KoReader.Get(deviceID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unknown device"))
		return
	}
	// the path's first id is the tag the sync named (see handleKoSync)
	tagID, err1 := strconv.ParseInt(r.PathValue("libraryId"), 10, 64)
	bookID, err2 := strconv.ParseInt(r.PathValue("bookId"), 10, 64)
	if err1 != nil || err2 != nil || !s.deviceReach(device, tagID, bookID) {
		http.NotFound(w, r)
		return
	}
	path, _, err := s.shelfFile(bookID)
	if err != nil || path == "" {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

// handleKoCover serves the cached cover to the plugin's mosaic view under the
// device token — the session-authed /api/v1/covers route is unreachable from
// a device.
func (s *Server) handleKoCover(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := s.koreaderDevice(w, r)
	if !ok {
		return
	}
	device, err := s.KoReader.Get(deviceID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unknown device"))
		return
	}
	tagID, err1 := strconv.ParseInt(r.PathValue("libraryId"), 10, 64)
	bookID, err2 := strconv.ParseInt(r.PathValue("bookId"), 10, 64)
	if err1 != nil || err2 != nil || !s.deviceReach(device, tagID, bookID) {
		http.NotFound(w, r)
		return
	}
	path := s.Covers.Path(bookID)
	if path == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

// ---- backups ----

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	backups, err := s.Backups.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": backups})
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	name, err := s.Backups.Create()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// handleRestoreBackup stages a restore and restarts. The staged db is
// swapped in by main() on the way back up.
func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if err := s.Backups.Restore(r.PathValue("name")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting to restore"})
	scheduleExit()
}

// ---- connection tests (hardcover token, goodreads shelf) ----

func (s *Server) handleTestHardcover(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" || token == "********" {
		token = s.Settings.Get("hardcover_token")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	hc := metadata.NewHardcover(func() string { return token })
	if err := hc.Test(ctx); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTestGoodreadsList validates a shelf before it's watched: parses
// whatever the user pasted and fetches the feed once.
func (s *Server) handleTestGoodreadsList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		SourceRef string `json:"sourceRef"`
		Shelf     string `json:"shelf"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ref, err := watcher.ParseGoodreadsRef(req.SourceRef, req.Shelf)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	userID, shelf, err := watcher.SplitGoodreadsRef(ref)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	entries, _, _, err := watcher.NewGoodreadsRSS().Fetch(ctx, userID, shelf, "")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": len(entries), "sourceRef": ref})
}

// ---- list discovery (pick shelves/lists instead of typing slugs) ----

func (s *Server) handleDiscoverGoodreads(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		SourceRef string `json:"sourceRef"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ref, err := watcher.ParseGoodreadsRef(req.SourceRef, "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	userID, _, err := watcher.SplitGoodreadsRef(ref)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	shelves, err := watcher.NewGoodreadsRSS().Shelves(ctx, userID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"userId": userID, "shelves": shelves})
}

// handleDiscoverHardcover lists Hardcover lists to watch: the token owner's
// own by default, or any user's PUBLIC lists when ?user= carries a username,
// @handle, or pasted hardcover.app URL.
func (s *Server) handleDiscoverHardcover(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	hc := metadata.NewHardcover(func() string { return s.Settings.Get("hardcover_token") })
	var lists []metadata.HCList
	var err error
	if user := strings.TrimSpace(r.URL.Query().Get("user")); user != "" {
		var username string
		username, err = metadata.ParseHardcoverUser(user)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		lists, err = hc.UserLists(ctx, username)
	} else {
		lists, err = hc.MyLists(ctx)
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lists": lists})
}

// ---- restart ----

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
	scheduleExit()
}

// scheduleExit ends the process shortly after the response flushes; the
// container's restart policy (or systemd, etc.) brings Booky back up.
func scheduleExit() {
	go func() {
		time.Sleep(500 * time.Millisecond)
		log.Printf("restarting on request")
		os.Exit(0)
	}()
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
