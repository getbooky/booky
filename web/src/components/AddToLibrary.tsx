import { useState } from "react"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Library as LibraryIcon } from "lucide-react"
import type { ApiLibrary } from "@/api"

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
