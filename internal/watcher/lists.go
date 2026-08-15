package watcher

import (
	"fmt"
	"log"
)

// WatchedList is one configured list: where books come from and where they go.
type WatchedList struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Kind             string `json:"kind"`      // "goodreads_rss" | "hardcover"
	SourceRef        string `json:"sourceRef"` // goodreads: "userID/shelf"; hardcover: list id
	LibraryID        int64  `json:"libraryId"`
	LibraryName      string `json:"libraryName,omitempty"`
	MonitorScope     string `json:"monitorScope"` // "book" | "series" | "author"
	OnRemove         string `json:"onRemove"`     // "nothing" | "unmonitor" | "delete"
	SearchOnAdd      bool   `json:"searchOnAdd"`
	Enabled          bool   `json:"enabled"`
	QualityProfileID int64  `json:"qualityProfileId,omitempty"` // 0 = library default
	LastChecked      string `json:"lastChecked,omitempty"`
	LastError        string `json:"lastError,omitempty"`
	ItemCount        int    `json:"itemCount"`
}

func validScope(s string) bool  { return s == "book" || s == "series" || s == "author" }
func validRemove(s string) bool { return s == "nothing" || s == "unmonitor" || s == "delete" }

// CreateList validates and stores a new watched list.
func (w *Watcher) CreateList(l WatchedList) (int64, error) {
	if l.Name == "" || l.SourceRef == "" || l.LibraryID == 0 {
		return 0, fmt.Errorf("name, sourceRef and libraryId are required")
	}
	if l.Kind != "goodreads_rss" && l.Kind != "hardcover" {
		return 0, fmt.Errorf("kind must be goodreads_rss or hardcover")
	}
	if l.MonitorScope == "" {
		l.MonitorScope = "book"
	}
	if l.OnRemove == "" {
		l.OnRemove = "nothing"
	}
	if !validScope(l.MonitorScope) || !validRemove(l.OnRemove) {
		return 0, fmt.Errorf("bad monitorScope or onRemove")
	}
	res, err := w.db.Exec(`INSERT INTO watched_lists
		(name, kind, source_ref, library_id, monitor_scope, on_remove, search_on_add, enabled, quality_profile_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.Name, l.Kind, l.SourceRef, l.LibraryID, l.MonitorScope, l.OnRemove, l.SearchOnAdd, l.Enabled, nullID(l.QualityProfileID))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateList rewrites a list's configuration (identity fields included — a
// changed source ref simply starts diffing against the new feed).
func (w *Watcher) UpdateList(l WatchedList) error {
	if !validScope(l.MonitorScope) || !validRemove(l.OnRemove) {
		return fmt.Errorf("bad monitorScope or onRemove")
	}
	res, err := w.db.Exec(`UPDATE watched_lists SET
		name = ?, source_ref = ?, library_id = ?, monitor_scope = ?, on_remove = ?,
		search_on_add = ?, enabled = ?, quality_profile_id = ?
		WHERE id = ?`,
		l.Name, l.SourceRef, l.LibraryID, l.MonitorScope, l.OnRemove,
		l.SearchOnAdd, l.Enabled, nullID(l.QualityProfileID), l.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("list %d not found", l.ID)
	}
	return nil
}

func (w *Watcher) DeleteList(id int64) error {
	_, err := w.db.Exec(`DELETE FROM watched_lists WHERE id = ?`, id)
	return err
}

func (w *Watcher) GetList(id int64) (*WatchedList, error) {
	lists, err := w.listQuery(id)
	if err != nil {
		return nil, err
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("list %d not found", id)
	}
	return &lists[0], nil
}

func (w *Watcher) Lists() ([]WatchedList, error) {
	return w.listQuery(0)
}

// listQuery loads lists; id 0 means all. Static SQL — the optional filter is
// disabled by passing 0, so no string concatenation is ever needed.
func (w *Watcher) listQuery(id int64) ([]WatchedList, error) {
	rows, err := w.db.Query(`
		SELECT wl.id, wl.name, wl.kind, wl.source_ref, wl.library_id, l.name,
		       wl.monitor_scope, wl.on_remove, wl.search_on_add, wl.enabled,
		       COALESCE(wl.quality_profile_id, 0), COALESCE(wl.last_checked, ''), wl.last_error,
		       (SELECT COUNT(*) FROM list_items li WHERE li.list_id = wl.id)
		FROM watched_lists wl JOIN libraries l ON l.id = wl.library_id
		WHERE (? = 0 OR wl.id = ?) ORDER BY wl.name`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchedList
	for rows.Next() {
		var l WatchedList
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.SourceRef, &l.LibraryID, &l.LibraryName,
			&l.MonitorScope, &l.OnRemove, &l.SearchOnAdd, &l.Enabled,
			&l.QualityProfileID, &l.LastChecked, &l.LastError, &l.ItemCount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// knownItems returns the external ids a list has already routed → book id.
func (w *Watcher) knownItems(listID int64) (map[string]int64, error) {
	rows, err := w.db.Query(`SELECT external_id, COALESCE(book_id, 0) FROM list_items WHERE list_id = ?`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var ext string
		var bookID int64
		if err := rows.Scan(&ext, &bookID); err != nil {
			return nil, err
		}
		out[ext] = bookID
	}
	return out, rows.Err()
}

func (w *Watcher) rememberItem(listID int64, externalID string, bookID int64, title string) error {
	_, err := w.db.Exec(`INSERT INTO list_items (list_id, external_id, book_id, title)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(list_id, external_id) DO UPDATE SET last_seen = datetime('now'), book_id = excluded.book_id`,
		listID, externalID, nullID(bookID), title)
	return err
}

func (w *Watcher) touchItem(listID int64, externalID string) error {
	_, err := w.db.Exec(`UPDATE list_items SET last_seen = datetime('now')
		WHERE list_id = ? AND external_id = ?`, listID, externalID)
	return err
}

func (w *Watcher) forgetItem(listID int64, externalID string) error {
	_, err := w.db.Exec(`DELETE FROM list_items WHERE list_id = ? AND external_id = ?`, listID, externalID)
	return err
}

func (w *Watcher) markChecked(listID int64, etag, lastError string) {
	if _, err := w.db.Exec(`UPDATE watched_lists SET last_checked = datetime('now'), etag = ?, last_error = ?
		WHERE id = ?`, etag, lastError, listID); err != nil {
		log.Printf("watcher: mark checked %d: %v", listID, err)
	}
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
