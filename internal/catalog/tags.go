package catalog

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Tag is a named audience. A book is visible to an account when they share a
// tag with it; a personal tag (OwnerUserID set) belongs to exactly one
// account and is where the things they add for themselves land.
type Tag struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	OwnerUserID int64  `json:"ownerUserId,omitempty"`
	// filled by ListTags
	BookCount int     `json:"bookCount"`
	MemberIDs []int64 `json:"memberIds"`
}

// Personal reports whether the tag is one account's own.
func (t Tag) Personal() bool { return t.OwnerUserID != 0 }

var (
	ErrTagNotFound = errors.New("tag not found")
	ErrTagPersonal = errors.New("personal tags are managed with their account")
)

func parseIDList(s string) []int64 {
	out := []int64{}
	for _, part := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// ListTags returns every tag with its book count and members, shared tags
// first by name, then personal tags by name.
func (s *Store) ListTags() ([]Tag, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.name, COALESCE(t.owner_user_id, 0),
		       (SELECT COUNT(*) FROM book_tags bt WHERE bt.tag_id = t.id),
		       COALESCE((SELECT group_concat(ut.user_id) FROM user_tags ut WHERE ut.tag_id = t.id), '')
		FROM tags t
		ORDER BY t.owner_user_id IS NOT NULL, t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		var members string
		if err := rows.Scan(&t.ID, &t.Name, &t.OwnerUserID, &t.BookCount, &members); err != nil {
			return nil, err
		}
		t.MemberIDs = parseIDList(members)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTag(id int64) (*Tag, error) {
	var t Tag
	err := s.db.QueryRow(`SELECT id, name, COALESCE(owner_user_id, 0) FROM tags WHERE id = ?`, id).
		Scan(&t.ID, &t.Name, &t.OwnerUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTagNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func cleanTagName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 {
		return "", errors.New("tag name must be 1–60 characters")
	}
	return name, nil
}

// CreateTag adds a shared tag.
func (s *Store) CreateTag(name string) (int64, error) {
	name, err := cleanTagName(name)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO tags (name) VALUES (?)`, name)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return 0, fmt.Errorf("a tag named %q already exists", name)
		}
		return 0, err
	}
	return res.LastInsertId()
}

// RenameTag renames a shared tag. Personal tags follow their account's
// username and aren't renamed here.
func (s *Store) RenameTag(id int64, name string) error {
	name, err := cleanTagName(name)
	if err != nil {
		return err
	}
	t, err := s.GetTag(id)
	if err != nil {
		return err
	}
	if t.Personal() {
		return ErrTagPersonal
	}
	if _, err := s.db.Exec(`UPDATE tags SET name = ? WHERE id = ?`, name, id); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return fmt.Errorf("a tag named %q already exists", name)
		}
		return err
	}
	return nil
}

// DeleteTag removes a shared tag. Books that only it shared become
// admin-only; nothing on disk is touched.
func (s *Store) DeleteTag(id int64) error {
	t, err := s.GetTag(id)
	if err != nil {
		return err
	}
	if t.Personal() {
		return ErrTagPersonal
	}
	_, err = s.db.Exec(`DELETE FROM tags WHERE id = ?`, id)
	return err
}

// BookTags returns the tags a book carries.
func (s *Store) BookTags(bookID int64) ([]int64, error) {
	return s.ids(`SELECT tag_id FROM book_tags WHERE book_id = ? ORDER BY tag_id`, bookID)
}

func (s *Store) ids(query string, arg any) ([]int64, error) {
	rows, err := s.db.Query(query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// TagChange is what a tag edit actually did, so callers (send-on-tag, the
// history log) react only to real changes.
type TagChange struct {
	BookID  int64
	Added   []int64
	Removed []int64
}

// AddBookTags gives a book tags, ignoring ones it already carries. An unknown
// tag id fails the whole call (foreign key), so a typo can't half-apply.
func (s *Store) AddBookTags(bookID int64, tagIDs []int64) (TagChange, error) {
	return s.editBookTags(bookID, tagIDs, nil)
}

// RemoveBookTags takes tags off a book.
func (s *Store) RemoveBookTags(bookID int64, tagIDs []int64) (TagChange, error) {
	return s.editBookTags(bookID, nil, tagIDs)
}

// SetBookTags replaces a book's tags with exactly tagIDs.
func (s *Store) SetBookTags(bookID int64, tagIDs []int64) (TagChange, error) {
	current, err := s.BookTags(bookID)
	if err != nil {
		return TagChange{}, err
	}
	want := map[int64]bool{}
	for _, id := range tagIDs {
		want[id] = true
	}
	var remove []int64
	for _, id := range current {
		if !want[id] {
			remove = append(remove, id)
		}
	}
	return s.editBookTags(bookID, tagIDs, remove)
}

func (s *Store) editBookTags(bookID int64, add, remove []int64) (TagChange, error) {
	ch := TagChange{BookID: bookID}
	tx, err := s.db.Begin()
	if err != nil {
		return ch, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	for _, id := range add {
		res, err := tx.Exec(`INSERT OR IGNORE INTO book_tags (book_id, tag_id) VALUES (?, ?)`, bookID, id)
		if err != nil {
			return TagChange{BookID: bookID}, fmt.Errorf("tag %d: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			ch.Added = append(ch.Added, id)
		}
	}
	for _, id := range remove {
		res, err := tx.Exec(`DELETE FROM book_tags WHERE book_id = ? AND tag_id = ?`, bookID, id)
		if err != nil {
			return TagChange{BookID: bookID}, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			ch.Removed = append(ch.Removed, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return TagChange{BookID: bookID}, err
	}
	return ch, nil
}

// ListTagIDs returns the tags a watched list gives its books.
func (s *Store) ListTagIDs(listID int64) ([]int64, error) {
	return s.ids(`SELECT tag_id FROM list_tags WHERE list_id = ? ORDER BY tag_id`, listID)
}

// SetListTags replaces the tags a watched list gives its books.
func (s *Store) SetListTags(listID int64, tagIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	if _, err := tx.Exec(`DELETE FROM list_tags WHERE list_id = ?`, listID); err != nil {
		return err
	}
	for _, id := range tagIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO list_tags (list_id, tag_id) VALUES (?, ?)`, listID, id); err != nil {
			return fmt.Errorf("tag %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// BookVisible reports whether a book carries any of tagIDs — the single rule
// every per-book check (web, OPDS, KoReader, Kindle) comes down to.
func (s *Store) BookVisible(bookID int64, tagIDs []int64) bool {
	if len(tagIDs) == 0 {
		return false
	}
	ph, args := idList(tagIDs)
	var n int
	err := s.db.QueryRow(fmt.Sprintf( //nolint:gosec // G201: placeholders only, see idList
		`SELECT COUNT(*) FROM book_tags WHERE book_id = ? AND tag_id IN (%s)`, ph),
		append([]any{bookID}, args...)...).Scan(&n)
	return err == nil && n > 0
}

// TheLibrary returns the one library every book lives in, or 0 before setup
// has created it.
func (s *Store) TheLibrary() int64 {
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM libraries ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		return 0
	}
	return id
}
