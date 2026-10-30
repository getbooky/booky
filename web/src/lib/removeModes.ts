// The ways a book can leave a shelf, worded once so the cover menu, the
// detail page and the bulk bar all offer the same choices.
export type RemoveMode = "library" | "file" | "block"

// For a plain user "library" means off THEIR shelf: their personal tag comes
// off and nothing else changes. For an admin it takes the book out of the
// library for everyone. Same mode on the wire; the server decides by role.
const USER_REMOVE = {
  mode: "library" as const,
  label: "Remove from my shelf",
  desc: "Takes it off your shelf. Anyone else it's shared with keeps it, and the file stays put.",
}

export const REMOVE_MODES: { mode: RemoveMode; label: string; desc: string; adminOnly?: boolean }[] = [
  {
    mode: "library",
    label: "Remove from library",
    desc: "Takes the book out of the library for everyone and clears who it's shared with. The file stays on disk, and it stays on its author and series pages.",
  },
  {
    mode: "file",
    label: "Delete & remove file",
    desc: "Also deletes the file from disk.",
    adminOnly: true,
  },
  {
    mode: "block",
    label: "Delete & block",
    desc: "Deletes the file too, and blocklists the book so watched lists can't re-add it.",
    adminOnly: true,
  },
]

// Anyone may take a book off their own shelf — a person who can add a series
// has to be able to unadd it. Unsharing it from others, touching files, and
// the install-wide blocklist are an admin's.
export const removeModesFor = (isAdmin: boolean) =>
  isAdmin ? REMOVE_MODES : [USER_REMOVE]

export const removeModeLabel = (mode: RemoveMode, isAdmin = true) =>
  removeModesFor(isAdmin).find(m => m.mode === mode)?.label ?? "Remove"

export const removeModeDesc = (mode: RemoveMode, isAdmin = true) =>
  removeModesFor(isAdmin).find(m => m.mode === mode)?.desc ?? ""

/** What to tell the person after a remove: a user's "off my shelf" can find
 * the book still shared with them through a tag. */
export const removeOutcome = (title: string, r: { status: string; detail?: string }) =>
  r.status === "shared" ? `${title}: ${r.detail ?? "still shared with you through a tag"}` : `${title} removed`
