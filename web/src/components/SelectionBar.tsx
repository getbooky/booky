import { useState } from "react"
import { api } from "@/api"
import type { ApiBook, ApiLibrary } from "@/api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Label } from "@/components/ui/label"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { FolderInput, Pencil, RefreshCw, Trash2, X } from "lucide-react"
import { removeModesFor } from "@/lib/removeModes"
import type { RemoveMode } from "@/lib/removeModes"
import { toast } from "sonner"
import { useIsAdmin } from "@/lib/access"

function BulkDeleteDialog({ open, onOpenChange, count, onConfirm }: {
  open: boolean; onOpenChange: (v: boolean) => void; count: number
  onConfirm: (mode: RemoveMode) => void
}) {
  const isAdmin = useIsAdmin()
  const [mode, setMode] = useState<RemoveMode>("library")
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md rounded-xl">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">Remove {count} book{count === 1 ? "" : "s"}</DialogTitle>
        </DialogHeader>
        <RadioGroup value={mode} onValueChange={v => setMode(v as RemoveMode)} className="gap-3 py-1">
          {removeModesFor(isAdmin).map(m => (
            <div key={m.mode} className="flex items-start gap-2.5">
              <RadioGroupItem value={m.mode} id={`bd-${m.mode}`} className="mt-0.5" />
              <Label htmlFor={`bd-${m.mode}`} className="cursor-pointer text-[13px] font-normal leading-snug">
                <b>{m.label}</b><br />
                <span className="text-muted-foreground">{m.desc}</span>
              </Label>
            </div>
          ))}
        </RadioGroup>
        <DialogFooter>
          <Button variant="outline" className="" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="destructive" className="" onClick={() => { onConfirm(mode); onOpenChange(false) }}>Remove</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function BulkEditDialog({ open, onOpenChange, count, onConfirm }: {
  open: boolean; onOpenChange: (v: boolean) => void; count: number
  onConfirm: (fields: Record<string, string>) => void
}) {
  const [language, setLanguage] = useState("")
  const [publisher, setPublisher] = useState("")
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md rounded-xl">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">Edit {count} book{count === 1 ? "" : "s"}</DialogTitle>
          <p className="text-xs text-muted-foreground">Filled fields apply to every selected book and lock against refreshes. Blank fields are left alone.</p>
        </DialogHeader>
        <div className="grid gap-4 py-1">
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">Language</div>
            <Input value={language} onChange={e => setLanguage(e.target.value)} placeholder="unchanged" />
          </div>
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">Publisher</div>
            <Input value={publisher} onChange={e => setPublisher(e.target.value)} placeholder="unchanged" />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" className="" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button className="" onClick={() => {
            const fields: Record<string, string> = {}
            if (language.trim()) fields.language = language.trim()
            if (publisher.trim()) fields.publisher = publisher.trim()
            if (Object.keys(fields).length === 0) { onOpenChange(false); return }
            onConfirm(fields); onOpenChange(false)
          }}>Apply</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// runs an async action over the selection, reporting a ok/fail tally
async function runBulk(books: ApiBook[], label: string, fn: (b: ApiBook) => Promise<unknown>) {
  let ok = 0, failed = 0
  let lastError = ""
  for (const b of books) {
    try { await fn(b); ok++ } catch (e) { failed++; lastError = e instanceof Error ? e.message : String(e) }
  }
  if (failed === 0) toast.success(`${label}: ${ok} book${ok === 1 ? "" : "s"}`)
  else toast.warning(`${label}: ${ok} done, ${failed} failed${lastError ? ` — ${lastError}` : ""}`)
}

export function SelectionBar({ books, libraries, onClear, onChanged }: {
  books: ApiBook[]
  libraries: ApiLibrary[]
  onClear: () => void
  onChanged: () => void
}) {
  const isAdmin = useIsAdmin()
  const [delOpen, setDelOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  if (books.length === 0) return null

  const guard = (fn: () => Promise<void>) => async () => {
    if (busy) return
    setBusy(true)
    try { await fn() } finally { setBusy(false); onChanged(); onClear() }
  }

  const doDelete = (mode: RemoveMode) =>
    guard(() => runBulk(books, "Removed", b => api.removeBook(b.libraryId ?? 0, b.id, mode)))()
  const doRefresh = guard(() => runBulk(books, "Refreshed", b => api.refreshBook(b.id)))
  const doEdit = (fields: Record<string, string>) =>
    guard(() => runBulk(books, "Edited", b => api.editBook(b.id, fields, true)))()
  const doMove = (to: ApiLibrary) =>
    guard(() => runBulk(books, `Moved to ${to.name}`, b => api.moveBook(b.id, b.libraryId ?? 0, to.id)))()

  return (
    <>
      <div className="fixed bottom-5 left-1/2 z-50 flex -translate-x-1/2 items-center gap-1.5 rounded-2xl border border-linesoft bg-surface2 px-3 py-2 shadow-[0_12px_40px_rgba(0,0,0,0.45)]">
        <span className="px-2 text-[13px] font-semibold">{books.length} selected</span>
        <span className="mx-1 h-5 w-px bg-linesoft" />
        {isAdmin && (
          <Button variant="ghost" size="icon" className="h-9 w-9 rounded-[10px]" title="Edit metadata"
            disabled={busy} onClick={() => setEditOpen(true)}>
            <Pencil className="h-4 w-4" />
          </Button>
        )}
        <Button variant="ghost" size="icon" className="h-9 w-9 rounded-[10px]" title="Refresh metadata"
          disabled={busy} onClick={doRefresh}>
          <RefreshCw className="h-4 w-4" />
        </Button>
        {isAdmin && libraries.length > 1 && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="h-9 w-9 rounded-[10px]" title="Move to library" disabled={busy}>
                <FolderInput className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent side="top" align="center" className="rounded-xl">
              {libraries.map(l => (
                <DropdownMenuItem key={l.id} onClick={() => doMove(l)}>{l.name}</DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        <Button variant="ghost" size="icon" className="h-9 w-9 rounded-[10px] text-want hover:text-want" title="Remove"
          disabled={busy} onClick={() => setDelOpen(true)}>
          <Trash2 className="h-4 w-4" />
        </Button>
        <span className="mx-1 h-5 w-px bg-linesoft" />
        <Button variant="ghost" size="icon" className="h-9 w-9 rounded-[10px]" title="Clear selection" onClick={onClear}>
          <X className="h-4 w-4" />
        </Button>
      </div>
      <BulkDeleteDialog open={delOpen} onOpenChange={setDelOpen} count={books.length} onConfirm={doDelete} />
      <BulkEditDialog open={editOpen} onOpenChange={setEditOpen} count={books.length} onConfirm={doEdit} />
    </>
  )
}
