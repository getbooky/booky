-- Bibliography cleanup (edition dedupe, ghost-record cutoff, stale-book
-- prune) improved across releases, but authors only re-sync weekly, so
-- catalogs seeded by older syncs keep rows the current sync would never
-- produce — visible as inflated series counts. Clearing the sync stamp sends
-- every author back through the sync loop once (one author per tick; provider
-- traffic stays gentle) and the stale-prune sweeps the leftovers.
UPDATE authors SET works_synced_at = NULL;
