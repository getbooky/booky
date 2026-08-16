import { useMemo, useState } from "react"
import { api, coverUrl } from "@/api"
import type { ApiBook, ApiLibrary, ApiSeries } from "@/api"
import { useApi } from "@/hooks/use-api"
import { AddToLibraryButton } from "@/components/AddToLibrary"
import { PickLibraryDialog } from "@/components/PickLibraryDialog"
import { Cover, MiniCover, hashColors } from "@/components/Cover"
import { Tag, Fmt } from "@/components/bits"
import { Switch } from "@/components/ui/switch"
import { Input } from "@/components/ui/input"
import { Search } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { bookRibbon } from "@/views/Library"

export function SeriesDetailView({ series, onBack, onOpenBook, onOpenAuthor }: {
  series: ApiSeries
  onBack: () => void
  onOpenBook: (b: ApiBook) => void
  onOpenAuthor: () => void
}) {
  const [query, setQuery] = useState("")
  const [descOpen, setDescOpen] = useState(false)
  const { data, loading, reload } = useApi(() => api.books({ seriesId: series.id }), 5_000)
  const books = useMemo(() =>
    [...(data?.books ?? [])].sort((a, b) => (a.seriesNum ?? 0) - (b.seriesNum ?? 0)),
    [data])
  const q = query.trim().toLowerCase()
  const shown = q ? books.filter(b => b.title.toLowerCase().includes(q)) : books

  const onShelf = books.filter(b => b.filePath).length
  const first = books[0]
  const [c1, c2] = first ? hashColors(first.title) : hashColors(series.name)

  const libsQuery = useApi(() => api.libraries())
  const libraries = libsQuery.data?.libraries ?? []
  const [shelving, setShelving] = useState(false)

  // Shelving the series names the library it goes into. The old control was a
  // "monitor series" switch that flipped a global flag and cascaded into every
  // library holding any of these books — so one person monitoring a series
  // started downloads on somebody else's shelf.
  const shelve = async (library: ApiLibrary) => {
    setShelving(true)
    try {
      const r = await api.addSeriesToLibrary(series.id, library.id)
      toast.success(r.queued > 0
        ? `Added to ${library.name} — searching for ${r.queued} missing ${r.queued === 1 ? "book" : "books"}`
        : `Added to ${library.name}`)
      reload()
    } catch (e) {
      toast.error(`${e instanceof Error ? e.message : e}`)
    } finally {
      setShelving(false)
    }
  }

  // Monitoring a catalog-only book is what puts it in a library — same
  // behavior as the author page's per-book toggle. One library needs no
  // asking; several open the picker rather than silently taking the first,
  // which dropped books onto somebody else's shelf.
  const [pickFor, setPickFor] = useState<ApiBook | null>(null)
  const monitorInto = async (b: ApiBook, library: ApiLibrary) => {
    try {
      await api.setBookMonitored(library.id, b.id, true)
      toast.success(`${b.title} added to ${library.name} — monitored`)
      setPickFor(null)
      reload()
    } catch (e) { toast.error(`${e instanceof Error ? e.message : e}`) }
  }
  const toggleBook = async (b: ApiBook, v: boolean) => {
    if (!b.libraryId) {
      if (!v) return // not shelved anywhere — nothing to unmonitor
      if (libraries.length === 0) {
        toast.error("Create a library first")
        return
      }
      if (libraries.length > 1) {
        setPickFor(b)
        return
      }
      await monitorInto(b, libraries[0])
      return
    }
    try {
      await api.setBookMonitored(b.libraryId, b.id, v)
      reload()
    } catch (e) { toast.error(`${e instanceof Error ? e.message : e}`) }
  }

  return (
    <section>
      <button onClick={onBack} className="mono-label mb-5 flex items-center gap-1.5 text-muted-foreground hover:text-brass">
        ← Series
      </button>

      {/* header */}
      <div className="flex flex-wrap items-start justify-between gap-5">
        <div>
          <h1 className="font-book text-[36px] font-bold leading-tight tracking-tight text-balance">{series.name}</h1>
          <button onClick={onOpenAuthor} className="mono-label text-faint hover:text-brass hover:underline">
            {series.author}
          </button>
          <div className="mt-2 flex items-center gap-3">
            <div className="flex w-[140px] gap-[3px]">
              {books.map((b, i) => (
                <span key={i} className={cn("h-2 flex-1 rounded-[2px]", b.filePath ? "bg-good" : "bg-surface3")} />
              ))}
            </div>
            <span className="mono-label text-muted-foreground">{onShelf} of {books.length} in library</span>
          </div>
        </div>
        <div className="pt-2">
          <AddToLibraryButton libraries={libraries} onPick={shelve} busy={shelving}
            label="Add series to library" className="h-9" />
        </div>
      </div>

      {/* first-in-series hero */}
      {first && (
        <div className="mt-7 flex flex-col gap-6 rounded-2xl border border-linesoft bg-surface p-5 md:flex-row">
          <div className="w-[150px] shrink-0">
            <Cover c1={c1} c2={c2} title={first.title} author={first.author}
              src={coverUrl(first.id)} ribbon={bookRibbon(first)}
              badge={first.fileFormat ? first.fileFormat.toUpperCase() : undefined} />
          </div>
          <div className="min-w-0 flex-1">
            <div className="mono-label text-faint">First in series</div>
            <button onClick={() => onOpenBook(first)}
              className="font-book mt-0.5 block text-left text-[22px] font-bold hover:text-brass">
              {first.title}
            </button>
            <div className="mt-1.5 flex flex-wrap items-center gap-2">
              {first.filePath ? <Tag kind="good">On shelf</Tag> : first.monitored ? <Tag kind="want">Missing</Tag> : <Tag kind="dim">Not monitored</Tag>}
              {first.releaseDate && <span className="text-xs text-muted-foreground">{first.releaseDate.slice(0, 4)}</span>}
              {first.fileFormat && <Fmt>{first.fileFormat.toUpperCase()}</Fmt>}
              {first.publisher && <span className="text-xs text-muted-foreground">{first.publisher}</span>}
            </div>
            <p className={cn("mt-3 max-w-[68ch] text-[13.5px] leading-relaxed text-muted-foreground", !descOpen && "line-clamp-4")}>
              {first.description || "No description cached yet — refresh metadata to fetch one."}
            </p>
            {(first.description?.length ?? 0) > 260 && (
              <button onClick={() => setDescOpen(o => !o)}
                className="mono-label mt-1.5 text-faint hover:text-brass">
                {descOpen ? "less ↑" : "more ↓"}
              </button>
            )}
          </div>
        </div>
      )}

      {/* the series, in order */}
      <div className="mb-2 mt-8 flex flex-wrap items-center justify-between gap-2">
        <div className="mono-label text-faint">In order</div>
        <div className="relative w-full max-w-[260px] sm:ml-auto">
          <Search className="absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
          <Input value={query} onChange={e => setQuery(e.target.value)}
            placeholder="Search books…" className="h-8 pl-9 text-[13px]" />
        </div>
      </div>
      <div className="border-t">
        {loading && <p className="mono-label py-4 text-faint">loading…</p>}
        {q && shown.length === 0 && !loading && (
          <p className="mono-label py-4 text-faint">no books match "{query.trim()}"</p>
        )}
        {shown.map(b => (
          <div key={`${b.id}-${b.libraryId}`} className="grid grid-cols-[34px_40px_1fr_auto] items-center gap-4 border-b border-linesoft px-1 py-2.5">
            <span className="font-label text-right text-xs text-faint">#{b.seriesNum ?? "—"}</span>
            <MiniCover c1={hashColors(b.title)[0]} c2={hashColors(b.title)[1]} src={coverUrl(b.id)} ribbon={bookRibbon(b)} />
            <div className="min-w-0">
              {/* w-full matters: a button's auto width hugs its content even
                  as display:block, so without it truncate never clips and
                  long titles paint under the status column */}
              <button className="block w-full max-w-full truncate text-left text-[13.5px] font-semibold hover:text-brass" onClick={() => onOpenBook(b)}>
                {b.title}
              </button>
              <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                {b.releaseDate?.slice(0, 4) ?? "—"}
                {b.fileFormat && <Fmt>{b.fileFormat.toUpperCase()}</Fmt>}
              </div>
            </div>
            <div className="flex items-center justify-end gap-2.5">
              {/* on phones the cover ribbon already carries the status —
                  the text pill would eat half the title's room */}
              {b.filePath
                ? <Tag kind="good" className="hidden sm:inline-block">On shelf</Tag>
                : <Tag kind={b.monitored ? "want" : "dim"} className="hidden sm:inline-block">{b.monitored ? "Missing" : "Not monitored"}</Tag>}
              <Switch checked={b.monitored} onCheckedChange={v => toggleBook(b, v)} aria-label={`Monitor ${b.title}`} />
            </div>
          </div>
        ))}
      </div>
      <PickLibraryDialog book={pickFor} libraries={libraries}
        onPick={l => { if (pickFor) void monitorInto(pickFor, l) }} onClose={() => setPickFor(null)} />
    </section>
  )
}
