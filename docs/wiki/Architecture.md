# Architecture

The technical shape of Booky, condensed. Decisions here were made
deliberately — change them on purpose, not by drift.

## Stack

Go backend (single binary) with the React + Tailwind + shadcn/ui frontend
embedded in it; SQLite in WAL mode; one Docker container, non-root
(PUID/PGID drop) on a minimal Alpine base. `/config` holds db, settings,
covers, backups, and logs; `/data` holds downloads and library roots so
imports are hardlink + rename.

Three background loops, all bounded:

- **List watcher** — polls watched lists (staggered, jittered, conditional
  requests).
- **Search engine** — triggered only (list add, monitor, manual, release day,
  backlog pass); never free-running.
- **Download tracker** — SABnzbd queue/history plus direct downloads, feeding
  the import pipeline.

## Data model

`Author → Series → Book → Editions`. A Book is one logical work; an Edition is
a specific ISBN/format; a Series is an ordered list of Books. Box sets and
omnibus editions are flagged `excluded` on editions at the metadata layer and
never appear as Books — that's the fix for the Readarr-class metadata failure.

Identity converges on the triple **Goodreads ID + ISBN-13 + Hardcover ID**,
written into every file so any rebuild re-matches exactly (see
[Metadata](Metadata.md)).

Bibliographies sync themselves: when an author appears by any add path, a
background task fetches their works — one paced provider query per author —
and monitored authors re-sync weekly. Bibliography books stay catalog-only
until someone explicitly shelves them.

## UI

"Reading room" design, dark-first with a light theme that follows the system
preference: warm ink/paper palette, brass accent, serif display for titles,
monospace catalog labels, ruled ledgers instead of floating cards. Status is a
bookmark ribbon over covers (green on shelf / blue downloading / red wanted).
The `web/` app is the design source of truth.

## Security constraints

Standing rules, not aspirations:

- All external strings — release names, scraped metadata, feed content — are
  hostile: sanitized before filesystem paths (no traversal out of roots),
  parameterized in SQL, escaped in the UI.
- Secrets live in the database/config only — never in code, fixtures, or logs.
- Path-taking APIs are admin-only and fenced to known roots with symlinks
  resolved.
- CI gates every push: golangci-lint (incl. gosec), race-detector tests,
  govulncheck, gitleaks secret scanning, a Docker build, and a Trivy image
  scan.
