-- Per-author monitor mode: what the weekly bibliography sync does with the
-- author's books.
--   selected  hand-picked books only (default) — new books arrive catalog-only
--   new       newly discovered books join the library monitored
--   all       the entire bibliography joins the library monitored
ALTER TABLE authors ADD COLUMN monitor_mode TEXT NOT NULL DEFAULT 'selected';
