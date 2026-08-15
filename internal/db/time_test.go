package db

import "testing"

// SQLite writes UTC with nothing in the string to say so, and a browser reads
// an unzoned stamp as local time — a whole feed off by the viewer's offset.
// The zone has to be on the wire.
func TestSQLTimeStampsTheZone(t *testing.T) {
	if got := SQLTime("2026-08-12 21:14:03"); got != "2026-08-12T21:14:03Z" {
		t.Errorf("SQLTime = %q, want RFC3339 UTC", got)
	}
	// anything not in SQLite's shape passes through rather than being mangled
	for _, s := range []string{"", "2026-08-12T21:14:03Z", "whenever"} {
		if got := SQLTime(s); got != s {
			t.Errorf("SQLTime(%q) = %q, want it untouched", s, got)
		}
	}
}
