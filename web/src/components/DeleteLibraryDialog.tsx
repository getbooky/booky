import { useState } from "react"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { api } from "@/api"
import type { ApiLibrary } from "@/api"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

type Mode = "keep" | "files"

// DeleteLibraryDialog asks the one question that matters: do the files on
// disk go too? Either way the library and its memberships are gone, so its
// books leave the Library view (they stay catalog-only on author and series
// pages, exactly like removing a single book).
export function DeleteLibraryDialog({ library, onOpenChange, onDeleted }: {
  library: ApiLibrary | null
  onOpenChange: (v: boolean) => void
  onDeleted: (l: ApiLibrary) => void
}) {
  const [mode, setMode] = useState<Mode>("keep")
  const [busy, setBusy] = useState(false)

  const confirm = async () => {
    if (!library) return
    setBusy(true)
    try {
      const r = await api.deleteLibrary(library.id, mode)
      toast.success(mode === "files"
        ? `${library.name} deleted — ${r.deletedFiles} file${r.deletedFiles === 1 ? "" : "s"} removed from disk`
        : `${library.name} deleted — files on disk untouched`)
      onDeleted(library)
      onOpenChange(false)
      setMode("keep")
    } catch (e) {
      toast.error(`Couldn't delete: ${e instanceof Error ? e.message : e}`)
    } finally {
      setBusy(false)
    }
  }

  const option = (v: Mode, title: string, desc: string) => (
    <button key={v} onClick={() => setMode(v)}
      className={cn(
        "rounded-lg border px-3.5 py-3 text-left transition-colors hover:border-brass/60",
        mode === v ? "border-brass bg-brass/10" : "border-linesoft"
      )}>
      <div className="flex items-center gap-2 text-[13.5px] font-semibold">
        <span className={cn(
          "flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full border",
          mode === v ? "border-brass" : "border-muted-foreground"
        )}>
          {mode === v && <span className="h-1.5 w-1.5 rounded-full bg-brass" />}
        </span>
        {title}
      </div>
      <p className="mt-1 pl-[22px] text-[12px] leading-relaxed text-muted-foreground">{desc}</p>
    </button>
  )

  return (
    <Dialog open={!!library} onOpenChange={v => { if (!v) { onOpenChange(false); setMode("keep") } }}>
      <DialogContent className="max-w-md rounded-xl">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">Delete {library?.name}?</DialogTitle>
          <p className="text-xs text-muted-foreground">
            {library ? `${library.bookCount} book${library.bookCount === 1 ? "" : "s"} · ${library.onShelf} on shelf · ${library.rootPath}` : ""}
          </p>
        </DialogHeader>
        <div className="grid gap-2 py-1">
          {option("keep", "Delete the library only",
            "Booky forgets the library and its books leave your shelves. Every file stays where it is on disk.")}
          {option("files", "Delete the library and its files",
            "Also deletes this library's book files from disk and cleans up the folders they emptied. This can't be undone.")}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button
            className={cn(mode === "files" && "bg-want text-white hover:bg-want/90")}
            disabled={busy} onClick={confirm}>
            {busy ? "Deleting…" : mode === "files" ? "Delete library & files" : "Delete library"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
