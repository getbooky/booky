# foliate-js (vendored)

Ebook rendering engine from https://github.com/johnfactotum/foliate-js
(MIT, see LICENSE), vendored at commit `78914aef4466eb960965702401634c2cb348e9b1`.

Only the modules the reader loads are included (EPUB / MOBI / AZW3 / FB2 /
CBZ paths plus the paginator and progress tracking). Local changes:

- `pdf.js` is replaced with a stub — upstream's version drags in a 13 MB
  pdfjs vendor tree and Booky's reader doesn't offer PDFs.
- `view.d.ts` is ours, typing just the surface `Reader.tsx` touches.

To update: copy the same file list from a newer upstream commit, re-apply the
`pdf.js` stub, and bump the commit hash above.

## Booky changes

Two small patches on top of that commit, both marked `Booky:` in the code:

- `epub.js`: scripts, event-handler attributes, `javascript:` URLs, frames,
  forms and `<base>` are removed from each chapter before it is shown. The
  reader's frames share the app's origin, so code in a book must never run.
  (The server's Content-Security-Policy forbids it too.)
- `view.js`: external links only open `http(s):` and `mailto:` addresses,
  with `noopener,noreferrer`.

Re-apply both when updating the vendored copy.
