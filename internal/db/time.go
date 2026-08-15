package db

import "time"

// sqliteStamp is what `datetime('now')` writes: UTC, but wearing no zone.
const sqliteStamp = "2006-01-02 15:04:05"

// SQLTime turns a stored timestamp into RFC3339 so clients can render it in
// their own timezone. It lives here, with the connection and the schema,
// because it is a fact about how this database writes time — every package
// that reads a timestamp column out to a client needs the same answer.
//
// Every created_at/updated_at column defaults to `datetime('now')`, which is
// UTC with nothing in the string to say so. Handed to a browser as-is, it is
// read as local time — "imported at 02:14" for something that happened at
// 21:14 last night. Stamping the zone on the way out is the whole fix; the
// column keeps its comparable, sortable shape.
//
// Anything that doesn't match the SQLite shape (already zoned, or empty) is
// passed through untouched.
func SQLTime(s string) string {
	t, err := time.ParseInLocation(sqliteStamp, s, time.UTC)
	if err != nil {
		return s
	}
	return t.Format(time.RFC3339)
}
