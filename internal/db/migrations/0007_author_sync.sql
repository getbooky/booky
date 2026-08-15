-- Bibliographies sync automatically in the background: this stamp records
-- when an author's works were last fetched, so the watcher can pace initial
-- syncs and re-sync monitored authors weekly (new releases appear on their
-- own).
ALTER TABLE authors ADD COLUMN works_synced_at TEXT;
