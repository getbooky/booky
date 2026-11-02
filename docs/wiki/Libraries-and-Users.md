# Library, Tags and Users

## One library, shared with tags

There is one library: a root folder holding **one file per book**. Who sees a
book is decided by **tags** — named audiences like "Kids" or "Household".

- A person sees a book when they hold one of its tags. Admins see everything,
  and a book with no tags is **admin-only**, so nothing reaches anyone by
  accident.
- Every account has a **personal tag** — "My shelf". What someone adds for
  themselves lands there.
- Admins share books from a book's page (**Shared with** — click a tag to
  share or unshare) or several at once (select books → **Tags**). Settings →
  Tags creates, renames and deletes tags and sets who holds them; Settings →
  Users does the same per person.
- **Watched lists** give their books the list's tags, and a new entry in a
  series you've shelved inherits the tags the rest of the series carries.
- The same rule covers every way a book can leave the server: the web app,
  the built-in reader, OPDS, KoReader devices and Kindle sends. Tags are read
  on every request, so taking one away takes effect on the next click — and
  on the next sync of that person's devices.

### Upgrading from per-person libraries

The first start after upgrading converts an install with several libraries:

- each library becomes a tag **of the same name**, each person keeps exactly
  the access they had, and each book carries the tags of every library it
  was in;
- the first library you created survives: its folder becomes the one folder,
  and files from the others move into it under the naming scheme. A copy that
  is a hardlink of, or byte-identical to, the kept one is removed; a copy that
  differs is set aside in `.booky-duplicates/` inside the library — never
  deleted. Folders left empty are cleaned up, and the log prints a one-line
  summary;
- the first library's quality profile becomes the profile.

There is no going back to the earlier version afterwards: back up `/config`
and your book folders before upgrading.

## Files

- **Imports are rename + move**: atomic and instant on one filesystem (keep
  everything under `/data`).
- **Naming** is one global scheme, default `{Author}/{Title}`, with tokens
  `{Author} {Title} {Series} {SeriesNum} {Year}` — the extension is added for
  you.
- **Existing collections** import in place: scan the library folder and match
  by ISBN/embedded metadata first, fuzzy filename second. Nothing is moved;
  uncertain matches land in a review queue (accept / pick manually / ignore).
  Scanned-in books start admin-only until you tag them.

## Roles

**Admins** own the install: settings, tags, quality profiles, watched lists,
backups, accounts, manual metadata edits, and deletions.

A **user** sees the books shared with their tags plus what they add for
themselves. They can browse, search and add books, refresh metadata, grab
releases, and pair their own KoReader and Kindle devices. **Remove** for a
user means "off my shelf": their personal tag comes off, nothing else changes,
and a book still shared with them through a tag stays. Changing tags,
deleting files and blocklisting stay with the admin.

## Visibility follows the books

You see an author or series because one of their books carries one of your
tags. Counts and cover fans narrow the same way. A bibliography sync leaves
what it finds catalog-only — browsable on the author page, in nobody's
library until someone shelves it.
