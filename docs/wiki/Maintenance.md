# Maintenance

## Backups

Booky zips its database and config to `/config/backups` on a schedule (default
weekly, keeping 4), and you can take a manual backup any time. Restore happens
from the UI — Booky exits and lets Docker restart it into the restored state,
which is why `--restart=unless-stopped` matters.

Since covers cache and settings all live under `/config`, backing that folder
up externally covers disaster recovery; library files under `/data` are yours
to back up with the rest of your data.

## Health and logs

There's no persistent health banner — a badge appears on the Settings nav only
when something actually fails. The Health panel lists the checks, the Logs
panel tails the app log with level filters, and a Restart button lives on
Settings.

## Manual import and uploads

Two routes for files Booky didn't download itself:

- **Browser upload** — from any device, phone included. Uploads are validated
  server-side by extension *and* file signature, so only real ebook files get
  through.
- **Server-side import** — point Booky at a path on the server. This one is
  admin-only and fenced to `/data`, the downloads directory, and library roots
  (symlinks resolved, config dir excluded), because a path-based API is a
  door you want locked.

Either way, import into any library from a book's page, or use a library's
**Import book** action for books not in Booky yet. Library scans of existing
folders match by embedded metadata first and land uncertain files in a review
queue instead of guessing.
