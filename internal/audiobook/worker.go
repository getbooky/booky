package audiobook

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getbooky/booky/internal/readalong"
)

// Worker builds read-alongs in the background, one at a time: speech
// recognition keeps a CPU core busy for the length of the book, and a home
// server has other work to do.
type Worker struct {
	Store *Store
	Tools readalong.Tools
	// ToolsErr, when set, says why read-alongs can't be built here (outside
	// the Docker image, say). Audiobooks still import and play.
	ToolsErr error
	// Dir holds one built read-along per book, by book id.
	Dir string
	// EPUBFor finds the book's EPUB, which the read-along is made from.
	EPUBFor func(bookID int64) (string, error)

	wake    chan struct{}
	mu      sync.Mutex
	running map[int64]context.CancelFunc
	once    sync.Once
}

// ErrNoEPUB means the book has no EPUB to read along with.
var ErrNoEPUB = errors.New("a read-along needs the book as an EPUB")

func (w *Worker) init() {
	w.once.Do(func() {
		w.wake = make(chan struct{}, 1)
		w.running = map[int64]context.CancelFunc{}
	})
}

// OutDir is where a book's read-along lives once built.
func (w *Worker) OutDir(bookID int64) string {
	return filepath.Join(w.Dir, strconv.FormatInt(bookID, 10))
}

// Wake tells the worker there is something new in the queue.
func (w *Worker) Wake() {
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Cancel stops a book's build if one is running; the store's generation
// check already keeps a stale build from recording anything.
func (w *Worker) Cancel(bookID int64) {
	w.init()
	w.mu.Lock()
	if cancel := w.running[bookID]; cancel != nil {
		cancel()
	}
	w.mu.Unlock()
}

// Forget stops any build and removes a book's read-along files.
func (w *Worker) Forget(bookID int64) {
	w.Cancel(bookID)
	if err := os.RemoveAll(w.OutDir(bookID)); err != nil {
		log.Printf("audiobook: remove read-along for book %d: %v", bookID, err)
	}
}

// Run works through the queue until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	w.init()
	if err := w.Store.resetInterrupted(); err != nil {
		log.Printf("audiobook: requeue interrupted builds: %v", err)
	}
	for {
		job, err := w.Store.Claim()
		if err != nil {
			log.Printf("audiobook: claim: %v", err)
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-w.wake:
			case <-time.After(time.Minute): // belt and braces
			}
			continue
		}
		w.build(ctx, job)
	}
}

func (w *Worker) build(parent context.Context, a *Audiobook) {
	ctx, cancel := context.WithCancel(parent)
	w.mu.Lock()
	w.running[a.BookID] = cancel
	w.mu.Unlock()
	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.running, a.BookID)
		w.mu.Unlock()
	}()

	fail := func(msg string, err error) {
		log.Printf("audiobook: read-along for book %d failed: %v", a.BookID, err)
		if serr := w.Store.fail(a, msg); serr != nil {
			log.Printf("audiobook: record failure: %v", serr)
		}
	}
	if w.ToolsErr != nil {
		fail("Read-along isn't available on this install: "+w.ToolsErr.Error(), w.ToolsErr)
		return
	}
	epub, err := w.EPUBFor(a.BookID)
	if err != nil {
		fail(ErrNoEPUB.Error()+" — add the EPUB and try again.", err)
		return
	}

	var last time.Time
	res, err := w.Tools.Build(ctx, readalong.Job{
		EPUB: epub, Audio: a.Path, Format: a.Format, OutDir: w.OutDir(a.BookID),
		Progress: func(phase string, done float64) {
			if time.Since(last) < 2*time.Second && done < 1 {
				return
			}
			last = time.Now()
			if err := w.Store.progress(a, phase, done); err != nil {
				log.Printf("audiobook: progress: %v", err)
			}
		},
	})
	switch {
	case ctx.Err() != nil:
		// replaced, removed, or shutting down: whoever cancelled has
		// already queued or dropped the row
		log.Printf("audiobook: read-along for book %d stopped", a.BookID)
	case errors.Is(err, readalong.ErrMismatch):
		fail(err.Error(), err)
	case err != nil:
		// tool output can name paths; the log keeps the detail, people get
		// a plain message
		fail("Couldn't process the audio — the server log has the details.", err)
	default:
		if err := w.Store.finish(a, res.Stats.Match, res.Duration); err != nil {
			log.Printf("audiobook: record result: %v", err)
		}
		log.Printf("audiobook: read-along for book %d ready: %d of %d sentences timed, %.0f%% matched",
			a.BookID, res.Stats.TimedUnits, res.Stats.Units, 100*res.Stats.Match)
	}
}

// Sweep removes what a crash or a deleted book left in Dir: half-finished
// builds, replaced builds, and read-alongs whose audiobook is gone.
func (w *Worker) Sweep() (int, error) {
	entries, err := os.ReadDir(w.Dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		stale := strings.HasPrefix(name, ".building-") || strings.HasSuffix(name, ".old")
		if id, err := strconv.ParseInt(name, 10, 64); err == nil && !stale {
			_, gerr := w.Store.Get(id)
			stale = errors.Is(gerr, ErrNone)
		}
		if !stale {
			continue
		}
		if err := os.RemoveAll(filepath.Join(w.Dir, name)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
