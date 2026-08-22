# Watched Lists

The loop that makes Booky feel automatic: add a book to a list you already
keep, and it shows up on your e-reader minutes later.

## Sources

- **Goodreads shelves** — via the public RSS feed. Paste your profile URL (the
  shelf must be public); Booky discovers your shelves with per-shelf book
  counts and you pick which to watch. The feed pages at 100 books, and Booky
  walks every page whenever the shelf changes — a 500-book shelf imports
  whole on the first sync, and books past the newest hundred are never
  mistaken for list removals. Unchanged shelves still cost one conditional
  request per poll. No scraping, no login.
- **Hardcover lists** — via the Hardcover API with your account token. Paste
  any profile URL or @username and pick from that user's public lists.

## Per-list configuration

| Setting | Default | Notes |
| --- | --- | --- |
| Target library | *required* | where new books land |
| Monitor scope | listed book | or whole series / author backlist |
| Search on add | on | grab immediately vs. just monitor |
| On remove | do nothing | Goodreads moves finished books off want-to-read — a list removal must never delete files unless you opt in |
| Quality profile | library's | overridable per list |

## Polling

Lists poll on a configurable interval (default 60s, floor 30s), staggered with
jitter so they don't stampede. RSS uses conditional requests (ETag); Hardcover
stays well inside its documented 60 req/min — one list check is one request.
