package catalog

import (
	"encoding/json"
	"fmt"

	"github.com/getbooky/booky/internal/db"
)

type QualityProfile struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Formats      []string `json:"formats"`
	CutoffFormat string   `json:"cutoffFormat"`
	// Languages is the accepted release-language list (newline/comma-
	// separated names, stored in the legacy `language` column). Empty means
	// the language filter is off for this profile.
	Languages      string `json:"languages"`
	PreferredTerms string `json:"preferredTerms"`
	AvoidedTerms   string `json:"avoidedTerms"`
}

func (s *Store) ListProfiles() ([]QualityProfile, error) {
	rows, err := s.db.Query(`SELECT id, name, formats, cutoff_format, language, preferred_terms, avoided_terms
		FROM quality_profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QualityProfile
	for rows.Next() {
		var p QualityProfile
		var formats string
		if err := rows.Scan(&p.ID, &p.Name, &formats, &p.CutoffFormat, &p.Languages, &p.PreferredTerms, &p.AvoidedTerms); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(formats), &p.Formats); err != nil {
			p.Formats = []string{"epub"}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProfile(id int64, name string, formats []string, cutoff, languages, preferred, avoided string) error {
	formatsJSON, err := json.Marshal(formats)
	if err != nil {
		return err
	}
	if cutoff == "" {
		cutoff = formats[0]
	}
	res, err := s.db.Exec(`UPDATE quality_profiles
		SET name = ?, formats = ?, cutoff_format = ?, language = ?, preferred_terms = ?, avoided_terms = ?
		WHERE id = ?`, name, string(formatsJSON), cutoff, languages, preferred, avoided, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("profile %d not found", id)
	}
	return nil
}

type HistoryItem struct {
	ID        int64  `json:"id"`
	BookID    int64  `json:"bookId,omitempty"`
	BookTitle string `json:"bookTitle,omitempty"`
	LibraryID int64  `json:"libraryId,omitempty"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// ListHistory returns the most recent events, narrowed to the account's
// libraries when a Visibility is given.
//
// The narrowing has to happen in SQL, not after: filtering a global "last 100"
// in Go would hand a scoped user whatever fraction of someone else's busy
// week happened to be theirs, and could show them an empty page while their
// own imports scrolled past just out of reach.
//
// Rows with no library are install-wide events — a bibliography sync finding
// new books, say, which names an author. NULL fails the IN test, so they drop
// out for scoped accounts and stay for admins, which is what we want either
// way: a user has no business seeing activity for authors they can't see, and
// everything they did themselves carries a library.
func (s *Store) ListHistory(limit int, v *Visibility) ([]HistoryItem, error) {
	ph, libArgs := idList(v.libraryIDs())
	args := []any{v.unscoped()}
	args = append(args, libArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(fmt.Sprintf( //nolint:gosec // G201: placeholders only, see idList
		`SELECT h.id, COALESCE(h.book_id, 0), COALESCE(b.title, ''), COALESCE(h.library_id, 0),
		        h.kind, h.detail, h.created_at
		 FROM history h LEFT JOIN books b ON b.id = h.book_id
		 WHERE (? = 1 OR h.library_id IN (%s))
		 ORDER BY h.id DESC LIMIT ?`, ph), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryItem
	for rows.Next() {
		var it HistoryItem
		if err := rows.Scan(&it.ID, &it.BookID, &it.BookTitle, &it.LibraryID, &it.Kind, &it.Detail, &it.CreatedAt); err != nil {
			return nil, err
		}
		it.CreatedAt = db.SQLTime(it.CreatedAt)
		out = append(out, it)
	}
	return out, rows.Err()
}
