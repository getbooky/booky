# Metadata

The metadata problem is why Booky exists: Readarr-style apps show box sets as
books, miss releases, and scramble series. Booky's answer has three parts —
canonical providers, stable identity, and aggressive bundle filtering.

## Providers

Hardcover drives every field: titles, series, descriptions, covers,
bibliographies (token from hardcover.app → settings → API, entered under
Settings → Metadata). Goodreads is only ever a *list source* — its entries are
matched against Hardcover and adopt the canonical record. Provider order is
user-rankable; the first provider with a value wins per field, others fill
gaps, and a failing provider degrades to the next automatically.

## Identity

Every Book converges on the triple **Goodreads ID + ISBN-13 + Hardcover ID**.
Matching runs on Goodreads ID/ISBN; the Hardcover link resolves by ISBN lookup,
falling back to title+author search with fuzzy verification, and a book that
can't link yet stays Goodreads-only and relinks on a later refresh. All three
identifiers are written into every file, so a rebuilt install re-matches
exactly.

## Box sets never appear as books

Omnibus editions, "Books 1–3" bundles, and collections are filtered at the
metadata layer — title patterns, compilation flags, page-count sanity — and
excluded everywhere: search results, bibliographies, watched-list adds. Your
own exclusion terms are editable pills under Settings → Metadata.

## Edit locks

Every book has a per-field editor. A manual edit auto-locks its field, and
refreshes never overwrite locked fields. Custom covers work the same way:
paste an image URL or upload a file, and the cover locks itself against
refreshes and regeneration until you unlock it.

## Writing into the files

Chosen fields are embedded into the EPUB itself using calibre conventions
(series fields readable by KoReader and calibre): title, author (+ sort name),
series (+ number), description, language, publisher, publication date, cover,
and identifiers — each group toggleable. A "write metadata to all library
files" bulk action applies the current records to everything on the shelf.

## Covers

Covers are fetched once per book and cached on disk under `/config` — browsing
never triggers external requests. A book whose cover isn't cached yet (or
doesn't exist, like an unreleased title) shows a clean gradient placeholder.
