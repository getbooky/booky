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
	ph, tagArgs := idList(v.tagIDs())
	args := []any{v.unscoped()}
	args = append(args, tagArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(fmt.Sprintf( //nolint:gosec // G201: placeholders only, see idList
		`SELECT h.id, COALESCE(h.book_id, 0), COALESCE(b.title, ''), COALESCE(h.library_id, 0),
		        h.kind, h.detail, h.created_at
		 FROM history h LEFT JOIN books b ON b.id = h.book_id
		 WHERE (? = 1 OR EXISTS (SELECT 1 FROM book_tags bt WHERE bt.book_id = h.book_id AND bt.tag_id IN (%s)))
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

// AudioProfile is the one audiobook profile: what to look for when Booky
// searches for a book's audiobook.
type AudioProfile struct {
	Formats        []string `json:"formats"` // best first: m4b, mp3, m4a
	Languages      string   `json:"languages"`
	PreferredTerms string   `json:"preferredTerms"`
	// AvoidedTerms skip a release outright (an abridged reading can't be
	// read along with the book), unlike the ebook profile's, which only
	// rank lower
	AvoidedTerms string `json:"avoidedTerms"`
	// MinSizeMB and MaxSizeMB bound a release's size; 0 is no limit
	MinSizeMB int64 `json:"minSizeMB"`
	MaxSizeMB int64 `json:"maxSizeMB"`
}

// AudioFormats are the formats an audiobook profile may list.
var AudioFormats = []string{"m4b", "mp3", "m4a"}

func (s *Store) GetAudioProfile() (AudioProfile, error) {
	var p AudioProfile
	var formats string
	err := s.db.QueryRow(`SELECT formats, language, preferred_terms, avoided_terms, min_size_mb, max_size_mb
		FROM audiobook_profile WHERE id = 1`).
		Scan(&formats, &p.Languages, &p.PreferredTerms, &p.AvoidedTerms, &p.MinSizeMB, &p.MaxSizeMB)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(formats), &p.Formats); err != nil || len(p.Formats) == 0 {
		p.Formats = append([]string(nil), AudioFormats...)
	}
	return p, nil
}

// UpdateAudioProfile saves the audiobook profile after checking it: known
// formats only, each once, and a size range that makes sense.
func (s *Store) UpdateAudioProfile(p AudioProfile) error {
	if len(p.Formats) == 0 {
		return fmt.Errorf("at least one format required")
	}
	seen := map[string]bool{}
	for _, f := range p.Formats {
		known := false
		for _, a := range AudioFormats {
			known = known || f == a
		}
		if !known || seen[f] {
			return fmt.Errorf("format %q isn't one of m4b, mp3, m4a (or is listed twice)", f)
		}
		seen[f] = true
	}
	if p.MinSizeMB < 0 || p.MaxSizeMB < 0 || (p.MaxSizeMB > 0 && p.MaxSizeMB < p.MinSizeMB) {
		return fmt.Errorf("the size range doesn't make sense")
	}
	formats, err := json.Marshal(p.Formats)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE audiobook_profile
		SET formats = ?, language = ?, preferred_terms = ?, avoided_terms = ?, min_size_mb = ?, max_size_mb = ?
		WHERE id = 1`, string(formats), p.Languages, p.PreferredTerms, p.AvoidedTerms, p.MinSizeMB, p.MaxSizeMB)
	return err
}
