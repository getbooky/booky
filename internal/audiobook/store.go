// Package audiobook keeps a book's audiobook: where its file is, and the
// read-along built from it in the background.
package audiobook

import (
	"database/sql"
	"errors"

	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/readalong"
)

// Read-along build states.
const (
	Queued  = "queued"
	Working = "working"
	Ready   = "ready"
	Failed  = "failed"
)

// Audiobook is one book's audiobook and the state of its read-along.
type Audiobook struct {
	BookID     int64            `json:"bookId"`
	Path       string           `json:"-"`
	Format     readalong.Format `json:"format"`
	Size       int64            `json:"size"`
	Duration   float64          `json:"duration"`
	AddedAt    string           `json:"addedAt"`
	Status     string           `json:"status"`
	Phase      string           `json:"phase,omitempty"`
	Progress   float64          `json:"progress"`
	Error      string           `json:"error,omitempty"`
	Match      float64          `json:"match,omitempty"`
	BuiltAt    string           `json:"builtAt,omitempty"`
	generation int64
}

// ErrNone means the book has no audiobook.
var ErrNone = errors.New("this book has no audiobook")

type Store struct{ db *sql.DB }

func NewStore(conn *sql.DB) *Store { return &Store{db: conn} }

const columns = `book_id, file_path, format, size, duration, added_at, status, phase, progress,
	error, match, COALESCE(built_at, ''), generation`

func scan(row interface{ Scan(...any) error }) (*Audiobook, error) {
	var a Audiobook
	err := row.Scan(&a.BookID, &a.Path, &a.Format, &a.Size, &a.Duration, &a.AddedAt, &a.Status,
		&a.Phase, &a.Progress, &a.Error, &a.Match, &a.BuiltAt, &a.generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNone
	}
	if err != nil {
		return nil, err
	}
	a.AddedAt, a.BuiltAt = db.SQLTime(a.AddedAt), db.SQLTime(a.BuiltAt)
	return &a, nil
}

func (s *Store) Get(bookID int64) (*Audiobook, error) {
	return scan(s.db.QueryRow(`SELECT `+columns+` FROM audiobooks WHERE book_id = ?`, bookID))
}

// Put records a book's audiobook file — new, or replacing the one before —
// and queues its read-along. The generation moves on, so a build still
// running for the old file can't record its result over the new one.
func (s *Store) Put(bookID int64, path string, format readalong.Format, size int64, duration float64) error {
	_, err := s.db.Exec(`INSERT INTO audiobooks (book_id, file_path, format, size, duration)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(book_id) DO UPDATE SET
			file_path = excluded.file_path, format = excluded.format, size = excluded.size,
			duration = excluded.duration, added_at = datetime('now'),
			status = 'queued', phase = '', progress = 0, error = '', match = 0,
			generation = generation + 1, queued_at = datetime('now'), built_at = NULL`,
		bookID, path, string(format), size, duration)
	return err
}

// Requeue asks for the read-along to be built again from the same file.
func (s *Store) Requeue(bookID int64) error {
	res, err := s.db.Exec(`UPDATE audiobooks SET status = 'queued', phase = '', progress = 0, error = '',
		generation = generation + 1, queued_at = datetime('now') WHERE book_id = ?`, bookID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNone
	}
	return nil
}

func (s *Store) Delete(bookID int64) error {
	_, err := s.db.Exec(`DELETE FROM audiobooks WHERE book_id = ?`, bookID)
	return err
}

// Claim takes the oldest queued build and marks it working.
func (s *Store) Claim() (*Audiobook, error) {
	a, err := scan(s.db.QueryRow(`UPDATE audiobooks SET status = 'working', phase = '', progress = 0, error = ''
		WHERE book_id = (SELECT book_id FROM audiobooks WHERE status = 'queued' ORDER BY queued_at, book_id LIMIT 1)
		RETURNING ` + columns))
	if errors.Is(err, ErrNone) {
		return nil, nil
	}
	return a, err
}

// The writes below land only while the build is the current one: same
// generation, still working.
const current = ` WHERE book_id = ? AND generation = ? AND status = 'working'`

func (s *Store) progress(a *Audiobook, phase string, done float64) error {
	_, err := s.db.Exec(`UPDATE audiobooks SET phase = ?, progress = ?`+current, phase, done, a.BookID, a.generation)
	return err
}

func (s *Store) finish(a *Audiobook, match, duration float64) error {
	_, err := s.db.Exec(`UPDATE audiobooks SET status = 'ready', phase = '', progress = 1, error = '',
		match = ?, duration = ?, built_at = datetime('now')`+current, match, duration, a.BookID, a.generation)
	return err
}

func (s *Store) fail(a *Audiobook, msg string) error {
	_, err := s.db.Exec(`UPDATE audiobooks SET status = 'failed', phase = '', error = ?`+current,
		msg, a.BookID, a.generation)
	return err
}

// resetInterrupted puts builds a restart cut short back in the queue.
func (s *Store) resetInterrupted() error {
	_, err := s.db.Exec(`UPDATE audiobooks SET status = 'queued', phase = '', progress = 0 WHERE status = 'working'`)
	return err
}

// Position is where an account is in an audiobook, in seconds.
func (s *Store) Position(userID, bookID int64) (float64, string, error) {
	var pos float64
	var at string
	err := s.db.QueryRow(`SELECT position, updated_at FROM listen_progress WHERE user_id = ? AND book_id = ?`,
		userID, bookID).Scan(&pos, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return pos, db.SQLTime(at), err
}

func (s *Store) SetPosition(userID, bookID int64, position float64) error {
	_, err := s.db.Exec(`INSERT INTO listen_progress (user_id, book_id, position) VALUES (?, ?, ?)
		ON CONFLICT(user_id, book_id) DO UPDATE SET position = excluded.position, updated_at = datetime('now')`,
		userID, bookID, position)
	return err
}
