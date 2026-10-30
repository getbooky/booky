import { useState } from "react"
import { api, kindle, tags as tagsApi } from "@/api"
import type { ApiBook, ApiKindleDevice } from "@/api"
import { TagPicker, useTags } from "@/components/TagPills"
import { Chips } from "@/components/bits"
import { useApi } from "@/hooks/use-api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Label } from "@/components/ui/label"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Mail, Pencil, RefreshCw, Tag as TagIcon, Trash2, X } from "lucide-react"
import { removeModesFor } from "@/lib/removeModes"
import type { RemoveMode } from "@/lib/removeModes"
import { toast } from "sonner"
import { useIsAdmin } from "@/lib/access"

// BulkTagDialog shares (or unshares) a selection. When the change would
// email more than a handful of books to Kindles, the server changes nothing
// and says how many — the dialog then asks before anything is sent, and
// "tag without sending" marks those books as handled so they never trickle
// out later.
function BulkTagDialog({ open, onOpenChange, books, onDone }: {
  open: boolean; onOpenChange: (v: boolean) => void; books: ApiBook[]; onDone: () => void
}) {
  const { tags } = useTags()
  const [mode, setMode] = useState<"add" | "remove" | "replace">("add")
  const [picked, setPicked] = useState<number[]>([])
  const [confirm, setConfirm] = useState<{ sends: number; devices: number } | null>(null)
  const [busy, setBusy] = useState(false)
  const ids = books.map(b => b.id)

  const run = async (sendToKindle?: boolean) => {
    setBusy(true)
    try {
      const r = await tagsApi.bulk(ids, picked, mode, sendToKindle)
      if (r.needsConfirm) {
        setConfirm({ sends: r.kindleSends, devices: r.kindleDevices })
        return
      }
      toast.success(`${books.length} book${books.length === 1 ? "" : "s"} updated`)
      setConfirm(null)
      setPicked([])
      onOpenChange(false)
      onDone()
    } catch (e) {
      toast.error(`Couldn't update sharing: ${e instanceof Error ? e.message : e}`)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={v => { if (!v) setConfirm(null); onOpenChange(v) }}>
      <DialogContent className="max-w-md rounded-xl">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">Tags · {books.length} book{books.length === 1 ? "" : "s"}</DialogTitle>
        </DialogHeader>
        {confirm ? (
          <div className="py-1 text-[13px] leading-relaxed">
            <p>
              This would email <b>{confirm.sends} book{confirm.sends === 1 ? "" : "s"}</b> to{" "}
              {confirm.devices === 1 ? "a Kindle" : `${confirm.devices} Kindles`} that auto-send{" "}
              {picked.length === 1 ? "this tag" : "these tags"}.
            </p>
            <p className="mt-2 text-muted-foreground">
              Tag without sending and those books won't go out later either — you can still send any of them by hand.
            </p>
          </div>
        ) : (
          <div className="grid gap-4 py-1">
            <Chips value={mode} onChange={setMode} options={[
              { v: "add", label: "Add tags" }, { v: "remove", label: "Remove tags" }, { v: "replace", label: "Replace all" },
            ]} />
            <TagPicker tags={tags} value={picked} onChange={setPicked} />
            {mode === "replace" && picked.length === 0 && (
              <p className="text-[12px] text-want">Replacing with no tags makes these books admin-only.</p>
            )}
          </div>
        )}
        <DialogFooter>
          {confirm ? (
            <>
              <Button variant="outline" disabled={busy} onClick={() => setConfirm(null)}>Back</Button>
              <Button variant="outline" disabled={busy} onClick={() => run(false)}>Tag without sending</Button>
              <Button disabled={busy} onClick={() => run(true)}>Tag &amp; send</Button>
            </>
          ) : (
            <>
              <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
              <Button disabled={busy || (picked.length === 0 && mode !== "replace")} onClick={() => run()}>
                {busy ? "Saving…" : "Apply"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

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

export function SelectionBar({ books, onClear, onChanged }: {
  books: ApiBook[]
  onClear: () => void
  onChanged: () => void
}) {
  const isAdmin = useIsAdmin()
  const [delOpen, setDelOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  const [tagOpen, setTagOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const { data: kindleData } = useApi(() => kindle.devices())
  const kindleDevices = (kindleData?.devices ?? []).filter(d => d.emailConfigured)
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
  // Bulk Send to Kindle mails what qualifies and says what didn't: only
  // EPUB/PDF files on the shelf go out, and the server refuses anything the
  // device's owner can't see. A selection with nothing sendable stays
  // selected — clearing it would just eat the picks.
  const doSend = (d: ApiKindleDevice) => {
    const sendable = books.filter(b =>
      !!b.filePath && ["epub", "pdf"].includes((b.fileFormat ?? "").toLowerCase()))
    if (sendable.length === 0) {
      toast.warning("Nothing to send — Kindle takes EPUB or PDF files")
      return
    }
    const skipped = books.length - sendable.length
    void guard(async () => {
      await runBulk(sendable, `Sent to ${d.name}`, b => kindle.sendBook(b.id, d.id))
      if (skipped > 0) toast(`${skipped} book${skipped === 1 ? "" : "s"} skipped — no EPUB or PDF file`)
    })()
  }

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
        {isAdmin && (
          <Button variant="ghost" className="h-9 gap-1.5 rounded-[10px] px-2.5 text-[13px]" title="Tags — who these books are shared with"
            disabled={busy} onClick={() => setTagOpen(true)}>
            <TagIcon className="h-4 w-4" /> Tags
          </Button>
        )}
        {kindleDevices.length === 1 && (
          <Button variant="ghost" size="icon" className="h-9 w-9 rounded-[10px]" title={`Send to Kindle — ${kindleDevices[0].name}`}
            disabled={busy} onClick={() => doSend(kindleDevices[0])}>
            <Mail className="h-4 w-4" />
          </Button>
        )}
        {kindleDevices.length > 1 && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="h-9 w-9 rounded-[10px]" title="Send to Kindle" disabled={busy}>
                <Mail className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent side="top" align="center" className="rounded-xl">
              {kindleDevices.map(d => (
                <DropdownMenuItem key={d.id} onClick={() => doSend(d)}>
                  {d.name} <span className="font-label ml-2 text-[10.5px] text-muted-foreground">{d.email}</span>
                </DropdownMenuItem>
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
      <BulkTagDialog open={tagOpen} onOpenChange={setTagOpen} books={books}
        onDone={() => { onChanged(); onClear() }} />
    </>
  )
}
