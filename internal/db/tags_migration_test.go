package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// openAt builds a database as the release before tags left it.
func openBeforeTags(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.Join(t.TempDir(), "old.db"))
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := migrateUntil(conn, "migrations/0024"); err != nil {
		t.Fatal(err)
	}
	return conn
}

func mustExec(t *testing.T, conn *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func ints(t *testing.T, conn *sql.DB, q string, args ...any) []int64 {
	t.Helper()
	rows, err := conn.Query(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// The tags migration must leave everyone with exactly the access they had:
// each library becomes a tag of the same id, each grant a membership, and
// each book the tags of every library it sat in. Books shelved in several
// libraries fold into one row that keeps the file, and nothing a Kindle would
// auto-send today goes out again.
func TestTagsMigrationKeepsEveryonesAccess(t *testing.T) {
	conn := openBeforeTags(t)

	mustExec(t, conn, `INSERT INTO quality_profiles (id, name, formats, cutoff_format) VALUES (1, 'p', '["epub"]', 'epub')`)
	mustExec(t, conn, `INSERT INTO libraries (id, name, root_path, quality_profile_id, opds_username, opds_password_hash)
		VALUES (1, 'Family', '/data/family', 1, 'fam', 'hash'), (2, 'Kids', '/data/kids', 1, 'kids', 'hash')`)
	mustExec(t, conn, `INSERT INTO users (id, username, password_hash, role) VALUES
		(1, 'root', 'x', 'admin'), (2, 'sam', 'x', 'user'), (3, 'kid', 'x', 'user')`)
	mustExec(t, conn, `INSERT INTO user_libraries (user_id, library_id) VALUES (2, 1), (2, 2), (3, 2)`)
	mustExec(t, conn, `INSERT INTO authors (id, name, sort_name) VALUES (1, 'A', 'A')`)
	mustExec(t, conn, `INSERT INTO books (id, author_id, title) VALUES (10, 1, 'Family only'), (11, 1, 'In both'), (12, 1, 'Kids only'), (13, 1, 'Wanted in both')`)
	mustExec(t, conn, `INSERT INTO library_books (library_id, book_id, monitored, file_path, file_format) VALUES
		(1, 10, 1, '/data/family/a.epub', 'epub'),
		(1, 11, 0, NULL, NULL),
		(2, 11, 1, '/data/kids/b.epub', 'epub'),
		(2, 12, 1, '/data/kids/c.epub', 'epub'),
		(1, 13, 1, NULL, NULL), (2, 13, 0, NULL, NULL)`)
	mustExec(t, conn, `INSERT INTO watched_lists (id, name, kind, source_ref, library_id) VALUES (1, 'kid list', 'hardcover', '1', 2)`)
	mustExec(t, conn, `INSERT INTO kindle_devices (id, owner_user_id, name, email, library_ids, auto_ids)
		VALUES (1, 3, 'Kid Kindle', 'k@kindle.com', '[2]', '[2]')`)
	mustExec(t, conn, `INSERT INTO devices (id, name, token, library_ids, auto_ids, owner_user_id)
		VALUES (1, 'Kobo', 'h', '[1,2]', '[2]', 2)`)

	if err := migrate(conn); err != nil {
		t.Fatalf("apply 0024: %v", err)
	}

	// libraries became tags of the same id
	if got := ints(t, conn, `SELECT id FROM tags WHERE owner_user_id IS NULL`); fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("shared tags = %v, want [1 2]", got)
	}
	// grants became memberships, plus a personal tag each
	if got := ints(t, conn, `SELECT tag_id FROM user_tags ut JOIN tags t ON t.id = ut.tag_id
		WHERE ut.user_id = 2 AND t.owner_user_id IS NULL`); fmt.Sprint(got) != "[1 2]" {
		t.Errorf("sam's tags = %v, want [1 2]", got)
	}
	if got := ints(t, conn, `SELECT tag_id FROM user_tags ut JOIN tags t ON t.id = ut.tag_id
		WHERE ut.user_id = 3 AND t.owner_user_id IS NULL`); fmt.Sprint(got) != "[2]" {
		t.Errorf("kid's tags = %v, want [2] — the kid must not gain Family", got)
	}
	for _, u := range []int64{1, 2, 3} {
		if got := ints(t, conn, `SELECT ut.tag_id FROM user_tags ut JOIN tags t ON t.id = ut.tag_id
			WHERE ut.user_id = ? AND t.owner_user_id = ?`, u, u); len(got) != 1 {
			t.Errorf("user %d personal tag = %v, want exactly one", u, got)
		}
	}
	// books carry every library they sat in
	want := map[int64]string{10: "[1]", 11: "[1 2]", 12: "[2]", 13: "[1 2]"}
	for book, w := range want {
		if got := ints(t, conn, `SELECT tag_id FROM book_tags WHERE book_id = ?`, book); fmt.Sprint(got) != w {
			t.Errorf("book %d tags = %v, want %s", book, got, w)
		}
	}
	// one library, one row per book, the file kept and monitoring kept
	if got := ints(t, conn, `SELECT id FROM libraries`); fmt.Sprint(got) != "[1]" {
		t.Fatalf("libraries = %v, want just the first", got)
	}
	var path sql.NullString
	var mon int
	if err := conn.QueryRow(`SELECT file_path, monitored FROM library_books WHERE book_id = 11`).Scan(&path, &mon); err != nil {
		t.Fatal(err)
	}
	if path.String != "/data/kids/b.epub" || mon != 1 {
		t.Errorf("book in both: path %q monitored %d — want the copy with a file, monitored", path.String, mon)
	}
	if got := ints(t, conn, `SELECT COUNT(*) FROM library_books GROUP BY book_id HAVING COUNT(*) > 1`); len(got) != 0 {
		t.Error("a book kept more than one library row")
	}
	// a list keeps feeding the same people
	if got := ints(t, conn, `SELECT tag_id FROM list_tags WHERE list_id = 1`); fmt.Sprint(got) != "[2]" {
		t.Errorf("list tags = %v, want [2]", got)
	}
	// nothing the kid's Kindle would auto-send today goes again
	if got := ints(t, conn, `SELECT book_id FROM kindle_sent WHERE device_id = 1`); fmt.Sprint(got) != "[11 12]" {
		t.Errorf("kindle backfill = %v, want the two Kids books already on disk", got)
	}
	// the old per-library OPDS logins stop working
	var hash string
	if err := conn.QueryRow(`SELECT opds_password_hash FROM libraries`).Scan(&hash); err != nil || hash != "unset" {
		t.Errorf("old OPDS login still set: %q %v", hash, err)
	}
	// devices keep their ids, now read as tags
	var libs string
	if err := conn.QueryRow(`SELECT library_ids FROM devices WHERE id = 1`).Scan(&libs); err != nil || !strings.Contains(libs, "2") {
		t.Errorf("KoReader device tags = %q %v", libs, err)
	}
}
