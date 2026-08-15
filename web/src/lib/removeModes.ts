// The three ways a book can leave a library, worded once so the cover menu,
// the detail page and the bulk bar all offer the same choices.
export type RemoveMode = "library" | "file" | "block"

export const REMOVE_MODES: { mode: RemoveMode; label: string; desc: string; adminOnly?: boolean }[] = [
  {
    mode: "library",
    label: "Delete",
    desc: "Removes the book from this library and leaves its file on disk. It stays on its author and series pages, unmonitored.",
  },
  {
    mode: "file",
    label: "Delete & remove file",
    desc: "Also deletes this library's copy from disk (other libraries' hardlinks are untouched).",
    adminOnly: true,
  },
  {
    mode: "block",
    label: "Delete & block",
    desc: "Deletes the file too, and blocklists the book so watched lists can't re-add it.",
    adminOnly: true,
  },
]

// Anyone may take a book off a shelf they can reach — a person who can add a
// series has to be able to unadd it. Touching files is another matter: the
// blocklist in particular is install-wide, so it reaches libraries the person
// may not even be able to see.
export const removeModesFor = (isAdmin: boolean) =>
  isAdmin ? REMOVE_MODES : REMOVE_MODES.filter(m => !m.adminOnly)

export const removeModeLabel = (mode: RemoveMode) =>
  REMOVE_MODES.find(m => m.mode === mode)?.label ?? "Delete"

export const removeModeDesc = (mode: RemoveMode) =>
  REMOVE_MODES.find(m => m.mode === mode)?.desc ?? ""
