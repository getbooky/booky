package acquire

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func insertQueueRow(t *testing.T, conn *sql.DB, bookID, libID int64, protocol, status, externalID string) int64 {
	t.Helper()
	res, err := conn.Exec(`INSERT INTO queue (book_id, library_id, release_title, source, protocol, status, external_id)
		VALUES (?, ?, 'First Ember EPUB', 'nzbs', ?, ?, ?)`, bookID, libID, protocol, status, externalID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func queueCount(t *testing.T, conn *sql.DB) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM queue`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func cancelledHistory(t *testing.T, conn *sql.DB) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM history WHERE kind = 'cancelled'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// sabRecorder answers SAB's api endpoint and records which delete modes
// were asked for. queueOK controls whether the queue delete acknowledges —
// a finished job has moved to history, and SAB says no from the queue side.
type sabRecorder struct {
	queueOK   bool
	historyOK bool
	calls     []string // "queue:del_files=1" style
}

func (s *sabRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode, name := r.URL.Query().Get("mode"), r.URL.Query().Get("name")
		if name != "delete" {
			_, _ = fmt.Fprint(w, `{"status": true}`)
			return
		}
		s.calls = append(s.calls, mode+":del_files="+r.URL.Query().Get("del_files"))
		ok := s.historyOK
		if mode == "queue" {
			ok = s.queueOK
		}
		_, _ = fmt.Fprintf(w, `{"status": %v}`, ok)
	}
}

// Cancelling a running usenet download deletes the SAB job from the queue
// with its partial files, drops the row, and leaves a history entry — with
// no blocklist and no cascade.
func TestCancelUsenetDeletesSabJob(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	conn := e.db
	sab := &sabRecorder{queueOK: true}
	ts := httptest.NewServer(sab.handler())
	defer ts.Close()
	must(t, cfg.Set("sab_url", ts.URL))
	must(t, cfg.Set("sab_api_key", "k"))

	id := insertQueueRow(t, conn, bookID, libID, "usenet", "downloading", "SABnzbd_nzo_1")
	if err := e.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(sab.calls) != 1 || sab.calls[0] != "queue:del_files=1" {
		t.Fatalf("SAB calls = %v, want one queue delete with files", sab.calls)
	}
	if queueCount(t, conn) != 0 {
		t.Fatal("queue row survived the cancel")
	}
	if cancelledHistory(t, conn) != 1 {
		t.Fatal("no cancelled history entry")
	}
	var blocked int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM blocklist`).Scan(&blocked); err == nil && blocked != 0 {
		t.Fatalf("cancel blocklisted the release: %d rows", blocked)
	}
}

// A job that finished between the click and the call has moved to SAB's
// history — the cancel follows it there.
func TestCancelFallsBackToHistoryDelete(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	conn := e.db
	sab := &sabRecorder{queueOK: false, historyOK: true}
	ts := httptest.NewServer(sab.handler())
	defer ts.Close()
	must(t, cfg.Set("sab_url", ts.URL))
	must(t, cfg.Set("sab_api_key", "k"))

	id := insertQueueRow(t, conn, bookID, libID, "usenet", "downloading", "SABnzbd_nzo_2")
	if err := e.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(sab.calls) != 2 || sab.calls[1] != "history:del_files=1" {
		t.Fatalf("SAB calls = %v, want queue then history", sab.calls)
	}
	if queueCount(t, conn) != 0 {
		t.Fatal("queue row survived the cancel")
	}
}

// If SAB can't be reached the row must stay: deleting it would orphan a
// live job that keeps downloading with nothing tracking it.
func TestCancelKeepsRowWhenSabUnreachable(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	conn := e.db
	must(t, cfg.Set("sab_url", "http://127.0.0.1:9"))
	must(t, cfg.Set("sab_api_key", "k"))

	id := insertQueueRow(t, conn, bookID, libID, "usenet", "downloading", "SABnzbd_nzo_3")
	if err := e.Cancel(context.Background(), id); err == nil {
		t.Fatal("cancel succeeded with SAB unreachable")
	}
	if queueCount(t, conn) != 1 {
		t.Fatal("queue row was deleted despite the live SAB job")
	}
}

// A direct download waiting for import (or failed importing) has its file
// on disk — cancel removes it along with the row.
func TestCancelRemovesWaitingFile(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	conn := e.db
	f := filepath.Join(t.TempDir(), "first-ember.epub")
	if err := os.WriteFile(f, []byte("book"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := insertQueueRow(t, conn, bookID, libID, "direct", "importing", f)
	if err := e.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("downloaded file survived the cancel")
	}
	if queueCount(t, conn) != 0 {
		t.Fatal("queue row survived the cancel")
	}
}

// A direct download in flight in this process is cut through its
// registered cancel func.
func TestCancelCutsInFlightDirectDownload(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	conn := e.db
	id := insertQueueRow(t, conn, bookID, libID, "direct", "downloading", "")

	ctx, cancelRun := context.WithCancel(context.Background())
	e.mu.Lock()
	e.running = map[int64]context.CancelFunc{id: cancelRun}
	e.mu.Unlock()

	if err := e.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("the in-flight download's context was not cancelled")
	}
	if queueCount(t, conn) != 0 {
		t.Fatal("queue row survived the cancel")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
