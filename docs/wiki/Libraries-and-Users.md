# Libraries and Users

## Libraries are root folders

A library is a root folder with its own quality profile and its own OPDS
credentials — two people, two libraries. Watched lists route their books into a
library; the sidebar shows each library with a live count.

- **Imports are hardlink + rename**: atomic, instant, and no duplicate bytes
  across libraries (same filesystem — keep everything under `/data`).
- **Cross-library dedupe**: there is one Book record per work. If a wanted
  book's file already exists in another library and satisfies the profile, it
  is hardlinked over — no search, no download, no extra disk. Deletes are
  per-library: they remove the link, not the shared bytes.
- **Naming** is one global scheme, default `{Author}/{Title}`, with tokens
  `{Author} {Title} {Series} {SeriesNum} {Year} {Format}`. Changing the scheme
  never moves files until an explicit "apply to existing" with preview.
- **Existing collections** import in place: scan and match by ISBN/embedded
  metadata first, fuzzy filename second. Nothing is moved; uncertain matches
  land in a review queue (accept / pick manually / ignore).

## Roles

**Admins** own the install: settings, libraries, quality profiles, watched
lists, backups, accounts, manual metadata edits, and deletions.

A **user** is given a specific set of libraries when created and only ever
sees those — other libraries are invisible, not merely read-only. Inside their
libraries they can browse, search and add books, refresh metadata, monitor
authors and series, grab releases, and pair their own KoReader device. They
can take a book back off their own shelf; deleting files and blocklisting stay
with the admin. Grants are re-read on every request, so revoking access takes
effect on the next click — and it also cuts off e-readers that user already
paired.

## Visibility follows the books

You see an author or series because one of their books sits in a library you
hold — an admin filing a book into your library brings its author and series
with it. Counts and cover fans narrow the same way.

Two rules keep shelves intentional:

- **Monitoring is not shelving.** Authors and series with a book on a shelf
  are always tracked, and bibliography syncs leave what they find
  catalog-only — browsable on the author page, in no library.
- **Shelving is explicit.** "Add series to library" names its destination
  (auto-selected when the account holds exactly one library), writes only that
  library, and remembers the opt-in so future entries in the series join the
  libraries that asked for it. Bulk actions are bounded to the caller's own
  libraries — a scoped user can't file books onto someone else's shelf.
