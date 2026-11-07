import { useEffect, useMemo, useState } from "react"
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Tag, Fmt } from "@/components/bits"
import { acquisition, audiobooks } from "@/api"
import type { ApiBook, ApiRelease, ApiSourceStat } from "@/api"
import { toast } from "sonner"
import { Download, Search, Zap } from "lucide-react"
import { cn } from "@/lib/utils"

function fmtSize(n?: number) {
  if (!n) return ""
  if (n > 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`
  return `${Math.round(n / 1024)} KB`
}

// The source a release came from, for the filter chips. Prowlarr results
// carry the indexer in source as "prowlarr:<indexer>"; collapse to the base.
function baseSource(r: ApiRelease): string {
  return r.source.split(":")[0]
}

const SOURCE_LABEL: Record<string, string> = {
  prowlarr: "Prowlarr", annas: "Anna's Archive", zlibrary: "Z-Library",
}
const label = (name: string) => SOURCE_LABEL[name] ?? name

// ReleasesDialog is the interactive search: every ranked candidate for a
// book, best first, filterable by source, with a grab button each — plus a
// one-click auto-grab that takes the top-ranked result.
export function ReleasesDialog({ book, open, onOpenChange, onGrabbed, kind = "ebook" }: {
  book: ApiBook | null
  open: boolean
  onOpenChange: (v: boolean) => void
  onGrabbed?: () => void
  /** what to search for: the ebook, or the audiobook */
  kind?: "ebook" | "audiobook"
}) {
  const audio = kind === "audiobook"
  const search = (b: ApiBook) => audio ? audiobooks.releases(b.id) : acquisition.releases(b.id, b.libraryId ?? 0)
  const [releases, setReleases] = useState<ApiRelease[] | null>(null)
  const [sources, setSources] = useState<ApiSourceStat[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")
  const [grabbing, setGrabbing] = useState<string | null>(null)
  const [autoGrabbing, setAutoGrabbing] = useState(false)
  const [filter, setFilter] = useState<string | null>(null) // null = all sources
  // results named for other titles stay hidden unless asked for
  const [showOthers, setShowOthers] = useState(false)
  const [loadedFor, setLoadedFor] = useState<number | null>(null) // book id the data belongs to

  useEffect(() => {
    if (!open || !book) return
    let live = true // a slow response for a previous book must not render here
    setReleases(null)
    setSources([])
    setError("")
    setFilter(null)
    setShowOthers(false)
    setLoading(true)
    search(book)
      .then(r => { if (live) { setReleases(r.releases ?? []); setSources(r.sources ?? []); setLoadedFor(book.id) } })
      .catch(e => { if (live) setError(e instanceof Error ? e.message : String(e)) })
      .finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [open, book]) // eslint-disable-line react-hooks/exhaustive-deps

  // Only trust the loaded data if it belongs to the book on screen right now —
  // closes the one-frame window where a previous book's releases could render
  // (and be grabbed) under a newly-opened book before the effect resets state.
  const fresh = loadedFor === (book?.id ?? -1)

  const matched = useMemo(() => (releases ?? []).filter(r => !r.titleMismatch), [releases])
  const others = (releases?.length ?? 0) - matched.length

  // sources that actually returned candidates for this book, for the filter row
  const present = useMemo(() => {
    const counts = new Map<string, number>()
    for (const r of matched) counts.set(baseSource(r), (counts.get(baseSource(r)) ?? 0) + 1)
    return counts
  }, [matched])

  const shown = useMemo(
    () => (fresh ? (showOthers ? releases ?? [] : matched) : []).filter(r => !filter || baseSource(r) === filter),
    [releases, matched, showOthers, filter, fresh],
  )

  const grab = async (rel: ApiRelease) => {
    if (!book) return
    if (rel.bundle && !window.confirm(audio
      ? "This download holds several books. Booky will split it, file this book's audiobook, and also add the audiobook to any other of its books you have that don't have one yet. Books you don't have are left out."
      : "This download holds several books. Booky will take the one named for this book, and refuse the download if it isn't in there.")) return
    setGrabbing(rel.downloadUrl)
    try {
      if (audio) await audiobooks.grab(book.id, rel)
      else await acquisition.grab(book.id, book.libraryId ?? 0, rel)
      toast.success(`Grabbed: ${rel.title}`)
      onOpenChange(false)
      onGrabbed?.()
    } catch (e) {
      toast.error(`Grab failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setGrabbing(null)
    }
  }

  const autoGrab = async () => {
    if (!book) return
    setAutoGrabbing(true)
    try {
      const r = audio ? await audiobooks.autoGrab(book.id) : await acquisition.autoGrab(book.id, book.libraryId ?? 0)
      if (r.grabbed) {
        toast.success("Grabbed the top-ranked release — watch Activity")
        onOpenChange(false)
        onGrabbed?.()
      } else {
        toast.warning("Nothing grabbable — every candidate failed or scored zero")
      }
    } catch (e) {
      toast.error(`Auto-grab failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setAutoGrabbing(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl rounded-xl">
        <DialogHeader>
          <div className="flex items-center justify-between gap-3">
            <DialogTitle className="font-book text-lg font-bold">{audio ? "Audiobook releases" : "Releases"} — {book?.title}</DialogTitle>
            <Button className="h-8 shrink-0" disabled={autoGrabbing || loading} onClick={autoGrab}>
              <Zap className="mr-1.5 h-3.5 w-3.5" />
              {autoGrabbing ? "Grabbing…" : "Auto-grab best"}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            {audio
              ? "Best match first, by the audiobook profile: format order, then preferred terms. Searched in Prowlarr's audiobook category; grabs go to SABnzbd, and the finished download is filed beside the ebook and lined up for read-along."
              : "Best match first: format order, then preferred terms, then source priority. \"Auto-grab best\" takes the top-ranked candidate; usenet goes to SABnzbd, direct sources download immediately."}
          </p>
        </DialogHeader>

        {/* per-source status: what was searched and how it fared */}
        {fresh && sources.length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5">
            <button
              onClick={() => setFilter(null)}
              className={cn("font-label rounded-md border px-2 py-1 text-[11px]", !filter ? "border-brass bg-brass/15 text-brass" : "text-muted-foreground hover:border-brass/50")}>
              All{releases ? ` (${matched.length})` : ""}
            </button>
            {sources.map(s => {
              const n = present.get(s.name) ?? 0
              // count what's actually shown (after torrent/blocklist filtering),
              // so the chip number never disagrees with the list
              const status = !s.configured ? "not set up" : s.error ? "failed" : `${n}`
              const disabled = n === 0 && !(showOthers && s.mismatched)
              return (
                <button key={s.name} disabled={disabled}
                  title={s.error || (!s.configured ? "Configure this source in Settings → Sources" : "")}
                  onClick={() => setFilter(filter === s.name ? null : s.name)}
                  className={cn(
                    "font-label rounded-md border px-2 py-1 text-[11px]",
                    filter === s.name ? "border-brass bg-brass/15 text-brass" : "text-muted-foreground hover:border-brass/50",
                    disabled && "opacity-50",
                    s.error && "border-want/50 text-want",
                  )}>
                  {label(s.name)} · {status}
                  {s.mismatched ? <span className="opacity-70"> (+{s.mismatched} other titles)</span> : null}
                </button>
              )
            })}
          </div>
        )}

        {(loading || !fresh) && !error && (
          <p className="flex items-center gap-2 py-4 text-[13px] text-muted-foreground">
            <Search className="h-3.5 w-3.5 animate-pulse" /> Searching indexers and direct sources…
          </p>
        )}
        {error && (
          <div className="rounded-r-lg border-l-[3px] border-want bg-want/10 px-4 py-3 text-[13px]">{error}</div>
        )}
        {fresh && releases && shown.length === 0 && !loading && (
          <p className="py-4 text-[13px] text-muted-foreground">
            {filter
              ? `No ${label(filter)} results for this book — try another source or "All".`
              : others > 0
                ? `None of the ${others} results is this book — they're named for other titles, usually the author's other books or the series. Nothing will be grabbed automatically; the weekly backlog pass keeps looking.`
                : "Nothing found right now — the weekly backlog pass keeps trying."}
          </p>
        )}
        {shown.length > 0 && (
          <div className="max-h-[420px] overflow-y-auto border-t">
            {shown.map(r => (
              <div key={r.downloadUrl + r.source} className="grid grid-cols-[1fr_auto] items-center gap-3 border-b border-linesoft px-1 py-2.5">
                <div className="min-w-0">
                  <div className={cn("truncate text-[13px] font-medium", r.titleMismatch && "text-muted-foreground")}>{r.title}</div>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                    {r.titleMismatch && <Tag kind="want">Different title?</Tag>}
                    {r.partOfSet && <Tag kind="want">Only part of the book</Tag>}
                    {r.bundle && <Tag kind="info">Several books</Tag>}
                    {r.format && <Fmt>{r.format.toUpperCase()}</Fmt>}
                    <Tag kind="dim">{r.indexer ?? label(baseSource(r))}</Tag>
                    <span>{r.protocol}</span>
                    {r.sizeBytes ? <span>· {fmtSize(r.sizeBytes)}</span> : null}
                  </div>
                </div>
                <Button className="h-8" variant={r.titleMismatch || r.bundle ? "outline" : "default"} disabled={grabbing !== null} onClick={() => grab(r)}>
                  <Download className="mr-1.5 h-3.5 w-3.5" />
                  {grabbing === r.downloadUrl ? "Grabbing…" : "Grab"}
                </Button>
              </div>
            ))}
          </div>
        )}
        {fresh && others > 0 && (
          <button className="w-fit text-[12.5px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            onClick={() => setShowOthers(v => !v)}>
            {showOthers
              ? "Hide results for other titles"
              : `Show ${others} result${others === 1 ? "" : "s"} for other titles`}
          </button>
        )}
      </DialogContent>
    </Dialog>
  )
}
