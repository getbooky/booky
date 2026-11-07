package api

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/getbooky/booky/internal/audiobook"
	"github.com/getbooky/booky/internal/koreader"
	"github.com/getbooky/booky/internal/readalong"
)

// Read-along for the KOReader plugin. The device downloads the read-along
// edition and the audio, then plays the audio and follows it in the text on
// its own. Every route takes the device token and the tag the sync named,
// exactly like a book download: a book the device can't reach doesn't exist.
//
// The audio goes out as raw PCM (readalong.DeviceRate, mono, s16le), decoded
// here: a Kindle's sound path takes nothing else, and the server has ffmpeg
// where the e-reader doesn't.

// deviceReadAlong resolves a device request to a book with a ready
// read-along. On failure it has already answered.
func (s *Server) deviceReadAlong(w http.ResponseWriter, r *http.Request) (*koreader.Device, int64, bool) {
	deviceID, ok := s.koreaderDevice(w, r)
	if !ok {
		return nil, 0, false
	}
	device, err := s.KoReader.Get(deviceID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unknown device"))
		return nil, 0, false
	}
	tagID, err1 := strconv.ParseInt(r.PathValue("libraryId"), 10, 64)
	bookID, err2 := strconv.ParseInt(r.PathValue("bookId"), 10, 64)
	if err1 != nil || err2 != nil || !s.deviceReach(device, tagID, bookID) {
		http.NotFound(w, r)
		return nil, 0, false
	}
	a, err := s.Audiobooks.Get(bookID)
	if err != nil || a.Status != audiobook.Ready {
		writeErr(w, http.StatusNotFound, errors.New("no read-along for this book yet"))
		return nil, 0, false
	}
	return device, bookID, true
}

// timing is the slice of timing.json a device needs.
type timing struct {
	Duration float64           `json:"duration"`
	Chunks   []readalong.Chunk `json:"chunks"`
	Units    []struct {
		ID    string  `json:"id"`
		Href  string  `json:"href"`
		Begin float64 `json:"begin"`
		End   float64 `json:"end"`
	} `json:"units"`
}

func (s *Server) loadTiming(bookID int64) (*timing, error) {
	data, err := os.ReadFile(filepath.Join(s.ReadAlong.OutDir(bookID), readalong.TimingFile)) //nolint:gosec // G703: built from a numeric id and a constant name
	if err != nil {
		return nil, err
	}
	var t timing
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// deviceUnit is one sentence as the plugin finds it: the engine knows it by
// "_doc_fragment_<frag>_ <id>".
type deviceUnit struct {
	ID    string  `json:"id"`
	Frag  int     `json:"frag"`
	Begin float64 `json:"begin"`
	End   float64 `json:"end"`
}

type deviceChunk struct {
	N        int     `json:"n"`
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
}

// GET /api/koreader/v1/readalong/{libraryId}/{bookId} — what to download
// and how to follow it.
func (s *Server) handleKoReadAlong(w http.ResponseWriter, r *http.Request) {
	_, bookID, ok := s.deviceReadAlong(w, r)
	if !ok {
		return
	}
	t, err := s.loadTiming(bookID)
	if err != nil {
		log.Printf("koreader: read-along timing for book %d: %v", bookID, err) //nolint:gosec // G706: a number and our own error
		writeErr(w, http.StatusNotFound, errors.New("no read-along for this book yet"))
		return
	}
	edition := filepath.Join(s.ReadAlong.OutDir(bookID), readalong.EditionFile)
	frags, err := readalong.SpineFragments(edition)
	if err != nil {
		log.Printf("koreader: read-along spine for book %d: %v", bookID, err) //nolint:gosec // G706: a number and our own error
		writeErr(w, http.StatusNotFound, errors.New("no read-along for this book yet"))
		return
	}
	book, err := s.Catalog.GetBook(bookID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	st, err := os.Stat(edition) //nolint:gosec // G703: built from a numeric id and a constant name
	if err != nil {
		http.NotFound(w, r)
		return
	}
	units := make([]deviceUnit, 0, len(t.Units))
	for _, u := range t.Units {
		frag, ok := frags[u.Href]
		if !ok {
			continue
		}
		units = append(units, deviceUnit{ID: u.ID, Frag: frag, Begin: u.Begin, End: u.End})
	}
	chunks := make([]deviceChunk, len(t.Chunks))
	for i, c := range t.Chunks {
		chunks[i] = deviceChunk{N: i + 1, Start: c.Start, Duration: c.Duration}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  1,
		"bookId":   bookID,
		"title":    book.Title,
		"author":   book.Author,
		"duration": t.Duration,
		// the edition changes when the read-along is rebuilt; the plugin
		// re-downloads when this does
		"edition": map[string]any{"size": st.Size(), "modified": st.ModTime().Unix()},
		"audio":   map[string]any{"format": "s16le", "rate": readalong.DeviceRate, "channels": 1},
		"chunks":  chunks,
		"units":   units,
	})
}

// GET /api/koreader/v1/readalong/{libraryId}/{bookId}/edition — the
// read-along EPUB.
func (s *Server) handleKoReadAlongEdition(w http.ResponseWriter, r *http.Request) {
	_, bookID, ok := s.deviceReadAlong(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/epub+zip")
	http.ServeFile(w, r, filepath.Join(s.ReadAlong.OutDir(bookID), readalong.EditionFile)) //nolint:gosec // G703: built from a numeric id and a constant name
}

var chunkFileName = regexp.MustCompile(`^[0-9]{4}\.mp3$`)

// GET /api/koreader/v1/readalong/{libraryId}/{bookId}/audio/{n} — chunk n
// (from 1) as raw PCM.
func (s *Server) handleKoReadAlongAudio(w http.ResponseWriter, r *http.Request) {
	_, bookID, ok := s.deviceReadAlong(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, err := s.loadTiming(bookID)
	if err != nil || n < 1 || n > len(t.Chunks) || !chunkFileName.MatchString(t.Chunks[n-1].File) {
		http.NotFound(w, r)
		return
	}
	src := filepath.Join(s.ReadAlong.OutDir(bookID), readalong.ChunkDir, t.Chunks[n-1].File)
	if _, err := os.Stat(src); err != nil { //nolint:gosec // G703: the chunk name passed chunkFileName above
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Booky-Audio", "s16le; rate="+strconv.Itoa(readalong.DeviceRate)+"; channels=1")
	// decoded as it's sent, so there's no length up front: the transfer is
	// chunked, and a cut connection shows on the device as an unfinished
	// stream rather than a short file
	if err := s.ReadAlong.Tools.DecodePCM(r.Context(), src, w); err != nil && r.Context().Err() == nil {
		log.Printf("koreader: decode chunk %d of book %d: %v", n, bookID, err) //nolint:gosec // G706: numbers and our own error
	}
}

// GET/PUT /api/koreader/v1/readalong/{libraryId}/{bookId}/position — where
// the device's owner is in the audiobook: the same place the web player
// and read-along use.
func (s *Server) handleKoReadAlongPosition(w http.ResponseWriter, r *http.Request) {
	device, bookID, ok := s.deviceReadAlong(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		pos, at := 0.0, ""
		if device.OwnerID != 0 {
			var err error
			if pos, at, err = s.Audiobooks.Position(device.OwnerID, bookID); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"position": pos, "updatedAt": at})
		return
	}
	var req struct {
		Position float64 `json:"position"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if math.IsNaN(req.Position) || math.IsInf(req.Position, 0) || req.Position < 0 || req.Position > 1e7 {
		writeErr(w, http.StatusBadRequest, errors.New("position must be seconds from the start"))
		return
	}
	// a device paired before accounts existed has nobody to save for
	if device.OwnerID != 0 {
		if err := s.Audiobooks.SetPosition(device.OwnerID, bookID, req.Position); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
