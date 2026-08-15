import { useEffect, useMemo, useRef, useState } from "react"
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { MiniCover, Cover, hashColors } from "@/components/Cover"
import { Tag } from "@/components/bits"

import { api } from "@/api"
import type { ApiLibrary, SearchResult } from "@/api"
import { toast } from "sonner"
import { ArrowLeft, Search } from "lucide-react"

// MonitorButton adds a result to a library. With one library it's a plain
// button — no questions asked; with several it opens a picker.
function MonitorButton({ libraries, onPick, label = "Monitor" }: {
  libraries: ApiLibrary[]
  onPick: (libraryId: number) => void
  label?: string
}) {
  if (libraries.length <= 1) {
    return (
      <Button variant="outline" className="h-8 shrink-0"
        onClick={() => libraries[0] ? onPick(libraries[0].id) : toast.error("Create a library first (Settings → Libraries)")}>
        {label}
      </Button>
    )
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" className="h-8 shrink-0">{label}</Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <div className="mono-label px-2 pb-1 pt-1.5 text-faint">Add to library</div>
        {libraries.map(l => (
          <DropdownMenuItem key={l.id} onClick={() => onPick(l.id)}>{l.name}</DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// ResultPreview: the read-before-you-monitor card. Details (description,
// series, identifiers) are enriched live across the provider chain, since
// list rows often carry only autocomplete-level data.
function ResultPreview({ result, libraries, onBack, onMonitor }: {
  result: SearchResult
  libraries: ApiLibrary[]
  onBack: () => void
  onMonitor: (r: SearchResult, libraryId: number) => void
}) {
  const [detail, setDetail] = useState<SearchResult>(result)
  const [loading, setLoading] = useState(true)
  useEffect(() => {
    let live = true
    api.enrich(result)
      .then(r => { if (live) setDetail({ ...result, ...r }) })
      .catch(() => { /* show what we have */ })
      .finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [result])

  const [c1, c2] = hashColors(detail.title)
  const year = detail.releaseDate?.slice(0, 4)

  return (
    <div className="px-5 py-4">
      <button onClick={onBack} className="mono-label mb-4 flex items-center gap-1.5 text-muted-foreground hover:text-brass">
        <ArrowLeft className="h-3.5 w-3.5" /> Results
      </button>
      <div className="flex gap-5">
        <div className="w-[120px] shrink-0">
          <Cover c1={c1} c2={c2} title={detail.title} author={(detail.authors ?? []).join(", ")} src={detail.coverUrl} />
        </div>
        <div className="min-w-0 flex-1">
          <h3 className="font-book text-xl font-bold leading-tight text-balance">{detail.title}</h3>
          <div className="mt-1 text-[13.5px] text-muted-foreground">{(detail.authors ?? []).join(", ")}</div>
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            {detail.seriesName && <Tag kind="dim">{detail.seriesName}{detail.seriesIndex ? ` #${detail.seriesIndex}` : ""}</Tag>}
            {year && <Tag kind="dim">{year}</Tag>}
            {detail.isbn13 && <Tag kind="dim">isbn {detail.isbn13}</Tag>}
          </div>
          <div className="mt-3">
            {detail.monitored
              ? <Tag kind="good">Already monitored</Tag>
              : detail.inLibrary
                ? <Tag kind="dim">Already in your library</Tag>
                : <MonitorButton libraries={libraries} onPick={id => onMonitor(detail, id)} />}
          </div>
        </div>
      </div>
      <div className="mt-4 max-h-[190px] overflow-y-auto border-t pt-3">
        <p className="text-[13px] leading-relaxed text-muted-foreground">
          {loading && !detail.description ? "fetching details…" : detail.description || "No description available from the providers."}
        </p>
      </div>
    </div>
  )
}

export function AddBookDialog({ open, onOpenChange, libraries, onAdded }: {
  open: boolean
  onOpenChange: (v: boolean) => void
  libraries: ApiLibrary[]
  onAdded: () => void
}) {
  const [q, setQ] = useState("")
  const [results, setResults] = useState<SearchResult[]>([])
  const [knownAuthors, setKnownAuthors] = useState<Record<string, boolean>>({})
  const [authorImages, setAuthorImages] = useState<Record<string, string>>({})
  const [searching, setSearching] = useState(false)
  const [preview, setPreview] = useState<SearchResult | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)

  // debounce keystrokes so each pause = one provider query
  useEffect(() => {
    if (!open) return
    clearTimeout(timer.current)
    const query = q.trim()
    if (query.length < 3) { setResults([]); setSearching(false); return }
    let stale = false
    timer.current = setTimeout(async () => {
      setSearching(true)
      try {
        const res = await api.search(query)
        if (stale) return // a newer query superseded this one
        setResults(res.results ?? [])
        setKnownAuthors(res.knownAuthors ?? {})
        setAuthorImages(res.authorImages ?? {})
      } catch (e) {
        if (!stale) toast.error(`Search failed: ${e instanceof Error ? e.message : e}`)
      } finally {
        if (!stale) setSearching(false)
      }
    }, 450)
    return () => { stale = true; clearTimeout(timer.current) }
  }, [q, open])

  const close = (v: boolean) => { onOpenChange(v); if (!v) setPreview(null) }

  const add = async (r: SearchResult, libraryId: number) => {
    try {
      const book = await api.addBook(r, libraryId, true)
      const libName = libraries.find(l => l.id === libraryId)?.name
      toast.success(`${book.title} added & monitored${libName && libraries.length > 1 ? ` in ${libName}` : ""}`)
      onAdded()
      close(false)
    } catch (e) {
      toast.error(`Couldn't add: ${e instanceof Error ? e.message : e}`)
    }
  }

  // Authors appearing in the results are addable themselves: their whole
  // bibliography syncs in the background and lands on the author page,
  // where individual books get monitored.
  const authorMatches = useMemo(() => {
    const seen = new Set<string>()
    const out: string[] = []
    const ql = q.trim().toLowerCase()
    for (const r of results) {
      for (const a of r.authors ?? []) {
        const key = a.toLowerCase()
        if (!a || seen.has(key)) continue
        seen.add(key)
        // rank authors whose name matches the query first
        if (key.includes(ql) || ql.includes(key)) out.unshift(a)
        else out.push(a)
      }
    }
    return out.slice(0, 3)
  }, [results, q])

  const addAuthor = async (name: string) => {
    try {
      await api.addAuthor(name)
      toast.success(`${name} added — bibliography is syncing, check their author page`)
      onAdded()
      close(false)
    } catch (e) {
      toast.error(`Couldn't add author: ${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      {/* anchored near the top (not centered) so the frame stays put while
          the result count changes under it */}
      <DialogContent className="top-[8vh] max-w-xl translate-y-0 gap-0 p-0 data-[state=closed]:slide-out-to-top-0 data-[state=open]:slide-in-from-top-0">
        {preview ? (
          <>
            <DialogHeader className="sr-only"><DialogTitle>Book details</DialogTitle></DialogHeader>
            <ResultPreview result={preview} libraries={libraries}
              onBack={() => setPreview(null)} onMonitor={add} />
          </>
        ) : (
          <>
            <DialogHeader className="border-b px-5 py-4">
              <DialogTitle className="font-book text-lg font-bold">Add to library</DialogTitle>
              <div className="relative mt-2">
                <Search className="absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
                <Input autoFocus value={q} onChange={e => setQ(e.target.value)}
                  placeholder="Title, author, or ISBN…" className="pl-9" />
              </div>
            </DialogHeader>
            <div className="h-[min(380px,60dvh)] overflow-y-auto px-2 py-2">
              {searching && <div className="mono-label px-3 py-4 text-faint">searching providers…</div>}
              {!searching && q.trim().length >= 3 && results.length === 0 && (
                <div className="px-3 py-8 text-center text-sm text-muted-foreground">No matches — try an ISBN or a fuller title.</div>
              )}
              {!searching && q.trim().length < 3 && (
                <div className="px-3 py-8 text-center text-sm text-faint">Type at least three characters.</div>
              )}
              {!searching && authorMatches.map(name => {
                const [c1, c2] = hashColors(name)
                const photo = authorImages[name.toLowerCase()]
                return (
                  <div key={`author-${name}`} className="flex items-center gap-3.5 rounded-sm px-3 py-2.5 hover:bg-surface2">
                    <span className="relative flex h-[54px] w-9 shrink-0 items-center justify-center overflow-hidden rounded-sm font-book text-lg font-bold text-paper"
                      style={{ background: `linear-gradient(160deg, ${c1}, ${c2})` }}>
                      {name.split(" ").map(p => p[0]).slice(0, 2).join("")}
                      {photo && (
                        <img src={photo} alt="" loading="lazy"
                          onError={e => { (e.target as HTMLImageElement).style.display = "none" }}
                          className="absolute inset-0 h-full w-full object-cover" />
                      )}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 text-[13.5px] font-semibold">
                        <span className="truncate">{name}</span>
                        <span className="font-label shrink-0 rounded-sm border px-1.5 text-[9px] uppercase tracking-[0.08em] text-brass">author</span>
                      </div>
                      <div className="truncate text-xs text-muted-foreground">Add the author — their bibliography fills in automatically</div>
                    </div>
                    {knownAuthors[name]
                      ? <Tag kind="good">Added</Tag>
                      : <Button variant="outline" className="h-8 shrink-0" onClick={() => addAuthor(name)}>
                          Add author
                        </Button>}
                  </div>
                )
              })}
              {results.map((r, i) => {
                const [c1, c2] = hashColors(r.title)
                return (
                  <div key={`${r.goodreadsId ?? r.hardcoverId ?? i}`} className="flex items-center gap-3.5 rounded-sm px-3 py-2.5 hover:bg-surface2">
                    <button className="flex min-w-0 flex-1 items-center gap-3.5 text-left" onClick={() => setPreview(r)}>
                      <MiniCover c1={c1} c2={c2} src={r.coverUrl} className="w-9" />
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-2 text-[13.5px] font-semibold">
                          <span className="truncate">{r.title}</span>
                          <span className="font-label shrink-0 rounded-sm border px-1.5 text-[9px] uppercase tracking-[0.08em] text-faint">{r.provider}</span>
                        </span>
                        <span className="block truncate text-xs text-muted-foreground">
                          {(r.authors ?? []).join(", ")}
                          {r.seriesName ? ` · ${r.seriesName}${r.seriesIndex ? ` #${r.seriesIndex}` : ""}` : ""}
                          {r.releaseDate ? ` · ${r.releaseDate.slice(0, 4)}` : ""}
                        </span>
                      </span>
                    </button>
                    {r.monitored
                      ? <Tag kind="good">Monitored</Tag>
                      : r.inLibrary
                        ? <Tag kind="dim">In library</Tag>
                        : <MonitorButton libraries={libraries} onPick={id => add(r, id)} />}
                  </div>
                )
              })}
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
