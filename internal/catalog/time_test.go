package catalog

import (
	"strings"
	"testing"
)

// The Activity feed renders these in the viewer's timezone, which only works
// if the zone survives the trip out of SQLite (see db.SQLTime).
func TestListHistoryReturnsZonedTimes(t *testing.T) {
	s := testStore(t)
	if err := s.AddHistory(0, 0, "added", "Burrow"); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListHistory(10, nil)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListHistory: %v %+v", err, items)
	}
	if !strings.HasSuffix(items[0].CreatedAt, "Z") || !strings.Contains(items[0].CreatedAt, "T") {
		t.Errorf("createdAt = %q, want RFC3339", items[0].CreatedAt)
	}
}
