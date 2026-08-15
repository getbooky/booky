# Search and Acquisition

Search fires **only** on: a list add, a manual monitor, a manual UI search,
release day, or the opt-in backlog pass. There is no free-running retry loop
hammering your indexers.

## Sources

- **Indexers via Prowlarr** — URL + API key under Settings → Sources; indexers
  sync automatically and searches proxy through Prowlarr.
- **Direct downloads** — built-in Anna's Archive and Z-Library providers with
  user-editable mirror lists (tried in order, testable from the UI), since
  those domains rotate. Direct grabs skip the download client and stream
  straight into the import pipeline.
- **Source priority** is one ordered list mixing indexers and direct
  providers, set in the Source Priority card.

## Ranking

Format order dominates (from the quality profile), then preferred/avoided
terms, then source priority among near-equals — a lower-priority source with a
better format beats a higher-priority source with a worse one. Rejected
candidates stay visible, greyed with the reason (box set, wrong language,
format not allowed, blocklisted), and can be force-grabbed.

## Quality profiles

A profile is an ordered list of allowed formats plus a cutoff (default
`EPUB > AZW3 > MOBI`, cutoff EPUB), language, and preferred/avoided terms.
Profiles are assigned per library and overridable per author, book, or watched
list. A grab below cutoff still imports — the book just stays on the upgrade
list until something better appears.

## Release-day automation

Monitored future books carry release dates (refreshed weekly — dates slip) and
sit on the Calendar. On release day each one is searched with a short taper
(day 0/1/2/3/7/14), then falls to the backlog pass.

The **backlog pass** is opt-in (default off): a rate-limited weekly re-search
of missing and cutoff-unmet books.

## When things fail

A failed download blocklists that release with a reason and automatically
cascades to the next-best candidate until something imports or the options run
out. The blocklist is visible (and un-blockable) in Activity; import failures
show their reason on the Activity row with **Retry import** and **Manual
import** actions right there.

## Download clients

- **SABnzbd** — URL, API key, category (default `booky`); history is polled
  for completions. The completed folder must live under `/data` so imports can
  hardlink.
- **Direct downloads** — built-in client with configurable parallelism.
- Torrent clients: interface reserved, post-v1.
