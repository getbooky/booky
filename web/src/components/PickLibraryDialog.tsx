import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Library as LibraryIcon } from "lucide-react"
import type { ApiBook, ApiLibrary } from "@/api"

// Monitoring a catalog-only book is what shelves it — and shelving names its
// destination. With one library there's nothing to ask; with several,
// silently picking the first put books on somebody else's shelf. This asks.
export function PickLibraryDialog({ book, libraries, onPick, onClose }: {
  book: ApiBook | null
  libraries: ApiLibrary[]
  onPick: (library: ApiLibrary) => void
  onClose: () => void
}) {
  return (
    <Dialog open={!!book} onOpenChange={v => { if (!v) onClose() }}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">Monitor into which library?</DialogTitle>
          <p className="text-xs text-muted-foreground">
            {book?.title} isn't on a shelf yet — pick whose library it joins.
          </p>
        </DialogHeader>
        <div className="flex flex-col gap-2 py-1">
          {libraries.map(l => (
            <Button key={l.id} variant="outline" className="h-10 justify-start" onClick={() => onPick(l)}>
              <LibraryIcon className="mr-2 h-4 w-4 text-muted-foreground" /> {l.name}
            </Button>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
