-- Grabs used to blocklist a release when no download client was set up
-- ("SABnzbd is not configured"), so the right release for a book stayed
-- refused after SABnzbd was configured. That was this install's setup, never
-- the release's fault: lift exactly those entries.
DELETE FROM blocklist
WHERE reason = 'SABnzbd is not configured'
   OR reason LIKE 'no download client for protocol %';
