// Package backup zips the database (plus loose config files) into
// /config/backups, prunes old archives, and stages restores. A restore never
// touches the live database: the archive's db is staged next to it and main()
// swaps it in on the next start, so a half-written file can't corrupt state.
package backup

import (
	"archive/zip"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	dirName = "backups"
	// StagedDBName is picked up by main() before the database opens.
	StagedDBName = "booky.db.restore"
)

type Manager struct {
	db        *sql.DB
	configDir string
}

func New(db *sql.DB, configDir string) *Manager {
	return &Manager{db: db, configDir: configDir}
}

func (m *Manager) dir() string { return filepath.Join(m.configDir, dirName) }

// ConfigDir is where the database, its WAL, and the backups live. Exposed so
// the manual-import fence can refuse to read out of it — a deployment that
// puts the config dir inside the media mount must not become a way to copy
// booky.db onto a shelf and download it.
func (m *Manager) ConfigDir() string { return m.configDir }

type Info struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	CreatedAt string `json:"createdAt"`
}

// Create writes a new archive and returns its name. The WAL is checkpointed
// first so the copied db file is complete on its own.
func (m *Manager) Create() (string, error) {
	if _, err := m.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return "", fmt.Errorf("checkpoint: %w", err)
	}
	if err := os.MkdirAll(m.dir(), 0o755); err != nil {
		return "", err
	}
	name := "booky-" + time.Now().UTC().Format("20060102-150405") + ".zip"
	path := filepath.Join(m.dir(), name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(f)

	fail := func(cause error) (string, error) {
		zw.Close()
		f.Close()
		os.Remove(path)
		return "", cause
	}

	// the db, plus any small loose config files at the top level — covers,
	// logs and existing backups are recreatable or already archives
	entries, err := os.ReadDir(m.configDir)
	if err != nil {
		return fail(err)
	}
	wrote := false
	for _, e := range entries {
		if e.IsDir() || !backupWorthy(e.Name()) {
			continue
		}
		if err := addFile(zw, filepath.Join(m.configDir, e.Name()), e.Name()); err != nil {
			return fail(err)
		}
		wrote = true
	}
	if !wrote {
		return fail(fmt.Errorf("nothing to back up in %s", m.configDir))
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return name, nil
}

// backupWorthy keeps the db and human-edited config; skips WAL siblings
// (checkpointed away), staged restores, and anything bulky.
func backupWorthy(name string) bool {
	switch {
	case name == "booky.db":
		return true
	case strings.HasSuffix(name, ".yml"), strings.HasSuffix(name, ".yaml"),
		strings.HasSuffix(name, ".json"), strings.HasSuffix(name, ".conf"):
		return true
	}
	return false
}

func addFile(zw *zip.Writer, path, name string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: info.ModTime()}
	dst, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return err
}

func (m *Manager) List() ([]Info, error) {
	entries, err := os.ReadDir(m.dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{
			Name:      e.Name(),
			SizeBytes: info.Size(),
			CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name }) // newest first
	return out, nil
}

// Prune keeps the newest keep archives and deletes the rest.
func (m *Manager) Prune(keep int) error {
	if keep < 1 {
		keep = 1
	}
	backups, err := m.List()
	if err != nil {
		return err
	}
	for i := keep; i < len(backups); i++ {
		if err := os.Remove(filepath.Join(m.dir(), backups[i].Name)); err != nil {
			return err
		}
	}
	return nil
}

// Restore stages the archive's database as StagedDBName. The caller restarts
// the process; main() swaps the staged file in before opening the db.
func (m *Manager) Restore(name string) error {
	// names come from our own listing; reject anything path-like outright
	if name != filepath.Base(name) || !strings.HasSuffix(name, ".zip") {
		return fmt.Errorf("bad backup name")
	}
	zr, err := zip.OpenReader(filepath.Join(m.dir(), name))
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "booky.db" {
			continue
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		defer src.Close()
		staged := filepath.Join(m.configDir, StagedDBName)
		dst, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		// bound the copy: a crafted archive can't fill the disk (db backups
		// are far below this)
		if _, err := io.Copy(dst, io.LimitReader(src, 4<<30)); err != nil {
			dst.Close()
			os.Remove(staged)
			return err
		}
		if err := dst.Close(); err != nil {
			os.Remove(staged)
			return err
		}
		return nil
	}
	return fmt.Errorf("backup %s has no database inside", name)
}

// SwapStaged replaces the live db file with a staged restore, if one exists.
// Called by main() before the database opens; WAL siblings are removed so
// the restored db starts clean.
func SwapStaged(configDir string) error {
	staged := filepath.Join(configDir, StagedDBName)
	if _, err := os.Stat(staged); os.IsNotExist(err) {
		return nil
	}
	dbPath := filepath.Join(configDir, "booky.db")
	for _, sibling := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(sibling); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(staged, dbPath)
}
