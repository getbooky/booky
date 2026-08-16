import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Library as LibraryIcon } from "lucide-react"
import type { ApiBook, ApiLibrary } from "@/api"

// Shelving names its destination — that's the whole point of the action, and
// what keeps one account's click out of another account's library. But naming
// it shouldn't be a chore: the library list is already scoped to whoever is
// asking, so when there's only one possible answer we just use it. A user with
// a single assigned library never sees a picker; an admin with several always
// does.
export function AddToLibraryButton({ libraries, onPick, busy, label = "Add to library", className }: {
  libraries: ApiLibrary[]
  onPick: (library: ApiLibrary) => void
  busy?: boolean
  label?: string
  className?: string
}) {
  const [open, setOpen] = useState(false)

  if (libraries.length === 0) return null

  if (libraries.length === 1) {
    return (
      <Button variant="outline" className={className} disabled={busy}
        onClick={() => onPick(libraries[0])}>
        <LibraryIcon className="mr-1.5 h-3.5 w-3.5" /> {label}
      </Button>
    )
  }
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" className={className} disabled={busy}>
          <LibraryIcon className="mr-1.5 h-3.5 w-3.5" /> {label}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="rounded-xl">
        {libraries.map(l => (
          <DropdownMenuItem key={l.id} onClick={() => onPick(l)}>{l.name}</DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// MonitorSwitch is the per-book monitor toggle anywhere a book might not be
// shelved yet. A shelved book (or a single-library install) toggles
// directly; an unshelved book with several libraries opens the same menu the
// Add-to-library buttons use — shelving names its destination, in the same
// clothes everywhere.
export function MonitorSwitch({ book, libraries, onToggle, onPick }: {
  book: ApiBook
  libraries: ApiLibrary[]
  onToggle: (b: ApiBook, v: boolean) => void
  onPick: (b: ApiBook, library: ApiLibrary) => void
}) {
  if (book.libraryId || libraries.length <= 1) {
    return <Switch checked={book.monitored} onCheckedChange={v => onToggle(book, v)} aria-label={`Monitor ${book.title}`} />
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Switch checked={false} aria-label={`Monitor ${book.title} — pick a library`} />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="rounded-xl">
        <DropdownMenuLabel className="mono-label text-faint">Monitor into</DropdownMenuLabel>
        {libraries.map(l => (
          <DropdownMenuItem key={l.id} onClick={() => onPick(book, l)}>{l.name}</DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
