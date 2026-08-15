import { useEffect, useState } from "react"
import { api } from "@/api"
import type { ApiLibrary, SearchResult } from "@/api"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { PathInput } from "@/components/PathInput"
import { Search, Upload } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

// ImportBookDialog is the library-level manual import: pick a file already
// on disk, identify which book it is (searched fresh — the book doesn't
// have to exist anywhere in Booky yet), and it's added to the library with
// the file delivered in one go. Monitoring is optional; a book with a file
// only needs it for quality upgrades.
export function ImportBookDialog({ open, onOpenChange, libraries, defaultLibraryId, onImported }: {
  open: boolean
  onOpenChange: (v: boolean) => void
  libraries: ApiLibrary[]
  // the library view being looked at — imports land there by default
  defaultLibraryId?: number
  onImported: () => void
}) {
  const [path, setPath] = useState("")
  const [file, setFile] = useState<File | null>(null)
  const [query, setQuery] = useState("")
  const [results, setResults] = useState<SearchResult[]>([])
  const [searching, setSearching] = useState(false)
  const [picked, setPicked] = useState<SearchResult | null>(null)
  const [libraryId, setLibraryId] = useState(0)
  const [monitor, setMonitor] = useState(false)
  const [importing, setImporting] = useState(false)
  const targetLib = libraryId || defaultLibraryId || libraries[0]?.id || 0

  useEffect(() => {
    if (!open) { setPath(""); setFile(null); setQuery(""); setResults([]); setPicked(null); setMonitor(false) }
  }, [open])

  // debounced identify-the-book search — same Hardcover-backed endpoint as
  // the add dialog, so never-seen books are fair game
  useEffect(() => {
    const q = query.trim()
    if (q.length < 3) { setResults([]); return }
    const t = window.setTimeout(async () => {
      setSearching(true)
      try {
        const res = await api.search(q)
        setResults((res.results ?? []).slice(0, 6))
      } catch { setResults([]) } finally { setSearching(false) }
    }, 400)
    return () => window.clearTimeout(t)
  }, [query])

  const run = async () => {
    if (!picked || (!path.trim() && !file) || !targetLib) return
    setImporting(true)
    try {
      // membership first (unmonitored so nothing races off to download),
      // then deliver the file; monitoring last, once the file exists
      const book = await api.addBook(picked, targetLib, false)
      const r = file
        ? await api.manualImportUpload(book.id, targetLib, file)
        : await api.manualImport(book.id, targetLib, path.trim())
      if (monitor) await api.setBookMonitored(targetLib, book.id, true)
      toast.success(`Imported → ${r.path}`)
      onImported()
      onOpenChange(false)
    } catch (e) {
      toast.error(`Import failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setImporting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">Import a book</DialogTitle>
          <p className="text-xs text-muted-foreground">
            Point at a file (or folder) under /data and tell Booky which book it is — it doesn't have to
            be in your library yet. The file is renamed by your naming scheme and delivered.
          </p>
        </DialogHeader>
        <div className="grid gap-4 py-1">
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">File or folder on the server</div>
            <PathInput value={path} onChange={setPath} placeholder="/data/downloads/…" />
            <div className="mt-2 flex items-center gap-2">
              <label className="inline-flex cursor-pointer items-center gap-1.5 text-[12.5px] text-muted-foreground hover:text-foreground">
                <Upload className="h-3.5 w-3.5" /> …or upload from this device
                <input type="file" accept=".epub,.azw3,.azw,.mobi,.pdf,.cbz,.cbr,.fb2" className="hidden"
                  onChange={e => { setFile(e.target.files?.[0] ?? null); setPath(""); e.target.value = "" }} />
              </label>
              {file && (
                <span className="flex items-center gap-1.5 rounded-lg border border-brass/40 bg-brass/10 px-2 py-0.5 text-[12px] text-brass">
                  {file.name}
                  <button type="button" onClick={() => setFile(null)} className="opacity-60 hover:opacity-100">×</button>
                </span>
              )}
            </div>
          </div>
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">Which book is it?</div>
            <div className="relative">
              <Search className="absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
              <Input value={query} onChange={e => { setQuery(e.target.value); setPicked(null) }}
                placeholder="Search by title or author…" className="h-9 pl-9 text-[13px]" />
            </div>
            {searching && <p className="mono-label mt-2 text-faint">searching…</p>}
            {results.length > 0 && (
              <div className="mt-2 flex max-h-[220px] flex-col gap-1 overflow-y-auto">
                {results.map((r, i) => (
                  <button key={i} type="button" onClick={() => setPicked(r)}
                    className={cn(
                      "rounded-lg border px-3 py-2 text-left text-[13px] transition-colors",
                      picked === r ? "border-brass bg-brass/10" : "border-linesoft hover:border-brass/40"
                    )}>
                    <span className="font-semibold">{r.title}</span>
                    <span className="ml-1.5 text-xs text-muted-foreground">
                      {(r.authors ?? []).join(", ")}{r.releaseDate ? ` · ${r.releaseDate.slice(0, 4)}` : ""}
                      {r.inLibrary ? " · already in library" : ""}
                    </span>
                  </button>
                ))}
              </div>
            )}
          </div>
          <div className="flex flex-wrap items-end gap-4">
            <div>
              <div className="mono-label mb-1.5 text-muted-foreground">Into library</div>
              <select className="h-9 rounded-md border border-input bg-transparent px-2.5 text-[12.5px]"
                value={targetLib} onChange={e => setLibraryId(Number(e.target.value))}>
                {libraries.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}
              </select>
            </div>
            <label className="flex cursor-pointer items-center gap-2 pb-2 text-[13px]">
              <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]"
                checked={monitor} onChange={e => setMonitor(e.target.checked)} />
              Monitor for upgrades
            </label>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button disabled={importing || !picked || (!path.trim() && !file) || !targetLib} onClick={run}>
            {importing ? (file ? "Uploading…" : "Importing…") : "Import"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
