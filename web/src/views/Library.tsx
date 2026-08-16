import { useMemo, useState } from "react"
import { api, acquisition, canRead, coverUrl } from "@/api"
import type { ApiBook, ApiLibrary } from "@/api"
import { useApi } from "@/hooks/use-api"
import { Cover, hashColors } from "@/components/Cover"
import type { RibbonKind } from "@/components/Cover"
import { Folio, Tag } from "@/components/bits"
import { EditMetadataDialog } from "@/components/BookDetail"
import { SelectionBar } from "@/components/SelectionBar"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator,
  DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  DropdownMenuCheckboxItem, DropdownMenuLabel,
} from "@/components/ui/dropdown-menu"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { FilterRows, fuzzyMatch } from "@/components/FilterRows"
import type { ScopeFilter } from "@/components/FilterRows"
import { ArrowDown, ArrowDownWideNarrow, ArrowUp, ArrowUpNarrowWide, BookDashed, BookOpen, BookOpenText, Filter, FolderInput, Image, Layers, MoreVertical, Pencil, RefreshCw, Search, Trash2, Zap } from "lucide-react"
import { ReleasesDialog } from "@/components/ReleasesDialog"
import { ImportBookDialog } from "@/components/ImportBookDialog"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { removeModesFor, removeModeDesc, removeModeLabel } from "@/lib/removeModes"
import type { RemoveMode } from "@/lib/removeModes"
import { useIsAdmin } from "@/lib/access"

export type Scope = "all" | "recent" | "missing" | "upgrade"

export function bookRibbon(b: ApiBook): RibbonKind {
  if (b.filePath) return "good"
  if (b.monitored) return "want"
  return "dim"
}

const SCOPE_TITLES: Record<Scope, string> = {
  all: "Library", recent: "Recently added", missing: "Missing", upgrade: "Upgrade wanted",
}

type SortKey = "added" | "title" | "author" | "release"

const SORT_LABELS: Record<SortKey, string> = {
  added: "Recently added", title: "Title", author: "Author", release: "Release date",
}

// each sort's natural first direction: dates newest-first, text A→Z
const SORT_DEFAULT_ASC: Record<SortKey, boolean> = {
  added: false, title: true, author: true, release: false,
}

// added_at is a lexically sortable timestamp; ties (one scan adds a batch in
// the same second) break by id so the order is stable and newest-ish wins
const byAdded = (a: ApiBook, b: ApiBook) =>
  (a.addedAt ?? "").localeCompare(b.addedAt ?? "") || a.id - b.id

const SORT_CMP: Record<SortKey, (a: ApiBook, b: ApiBook) => number> = {
  added: byAdded,
  title: (a, b) => a.title.localeCompare(b.title),
  author: (a, b) => a.author.localeCompare(b.author) || (a.seriesNum ?? 0) - (b.seriesNum ?? 0),
  release: (a, b) => (a.releaseDate ?? "").localeCompare(b.releaseDate ?? ""),
}

function BookCell({ book, libraries, seriesTag, bust, selected, selectionActive, onToggleSelect, onOpen, onOpenAuthor, onOpenSeries, onRead, onEdit, onSearch, onChanged }: {
  book: ApiBook
  libraries: ApiLibrary[]
  seriesTag?: boolean
  bust?: number
  selected: boolean
  selectionActive: boolean
  onToggleSelect: (b: ApiBook) => void
  onOpen: (b: ApiBook) => void
  onOpenAuthor: (b: ApiBook) => void
  onOpenSeries: (b: ApiBook) => void
  onRead: (b: ApiBook) => void
  onEdit: (b: ApiBook) => void
  onSearch: (b: ApiBook) => void
  onChanged: () => void
}) {
  const [c1, c2] = hashColors(book.title)
  const isAdmin = useIsAdmin()
  const otherLibs = libraries.filter(l => l.id !== book.libraryId)

  const refreshMeta = async () => {
    toast(`Refreshing ${book.title}…`)
    try {
      await api.refreshBook(book.id)
      toast.success("Metadata refreshed")
      onChanged()
    } catch (e) { toast.error(`${e instanceof Error ? e.message : e}`) }
  }
  const regenCover = async () => {
    toast("Regenerating cover…")
    try {
      await api.regenCover(book.id)
      toast.success("Cover regenerated")
      onChanged()
    } catch (e) { toast.error(`${e instanceof Error ? e.message : e}`) }
  }
  const move = async (to: ApiLibrary) => {
    try {
      await api.moveBook(book.id, book.libraryId ?? 0, to.id)
      toast.success(`Moved to ${to.name}`)
      onChanged()
    } catch (e) { toast.error(`${e instanceof Error ? e.message : e}`) }
  }
  const autoGrab = async () => {
    if (!book.libraryId) { toast.error("Monitor this book into a library first"); return }
    toast(`Auto-grabbing ${book.title}…`)
    try {
      const r = await acquisition.autoGrab(book.id, book.libraryId)
      if (r.grabbed) toast.success("Grabbed the top-ranked release — watch Activity")
      else toast.warning("Nothing grabbable right now")
      onChanged()
    } catch (e) { toast.error(`Auto-grab failed: ${e instanceof Error ? e.message : e}`) }
  }
  const remove = async (mode: RemoveMode) => {
    if (!book.libraryId) return
    if (!window.confirm(`${book.title}\n\n${removeModeLabel(mode)}?\n${removeModeDesc(mode)}`)) return
    try {
      await api.removeBook(book.libraryId, book.id, mode)
      toast(`${book.title} removed`)
      onChanged()
    } catch (e) { toast.error(`Couldn't remove: ${e instanceof Error ? e.message : e}`) }
  }

  return (
    <div className="group/cell relative flex flex-col gap-2">
      <div className={cn("relative cursor-pointer rounded-[8px_12px_12px_8px]", selected && "ring-2 ring-brass ring-offset-2 ring-offset-background")} role="button" tabIndex={0}
        onClick={() => (selectionActive ? onToggleSelect(book) : onOpen(book))}
        onKeyDown={e => { if (e.key === "Enter") (selectionActive ? onToggleSelect : onOpen)(book) }}>
        <Cover
          c1={c1} c2={c2} title={book.title} author={book.author}
          src={coverUrl(book.id) + (bust ? `?v=${bust}` : "")}
          ribbon={bookRibbon(book)}
          badge={book.fileFormat ? book.fileFormat.toUpperCase() : undefined}
        />
        <button aria-label={selected ? `Deselect ${book.title}` : `Select ${book.title}`}
          onClick={e => { e.stopPropagation(); onToggleSelect(book) }}
          className={cn(
            "absolute right-1.5 top-9 z-10 flex h-6 w-6 items-center justify-center rounded-md border transition-opacity",
            selected
              ? "border-brass bg-brass text-brass-ink opacity-100"
              : "border-white/50 bg-black/45 text-transparent opacity-0 backdrop-blur-xs hover:border-white focus-visible:opacity-100 group-hover/cell:opacity-100",
            selectionActive && "opacity-100"
          )}>
          <svg viewBox="0 0 24 24" width="13" height="13" style={{ stroke: "currentColor", fill: "none", strokeWidth: 3 }}><path d="M5 13l4 4 10-11" /></svg>
        </button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button aria-label={`${book.title} options`} onClick={e => e.stopPropagation()}
              className="absolute left-1.5 top-1.5 z-10 flex h-7 w-7 items-center justify-center rounded-lg bg-black/60 text-white/90 opacity-0 backdrop-blur-xs transition-opacity hover:bg-black/80 focus-visible:opacity-100 group-hover/cell:opacity-100 data-[state=open]:opacity-100">
              <MoreVertical className="h-4 w-4" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="min-w-[190px] rounded-xl" onClick={e => e.stopPropagation()}>
            {canRead(book) && (
              <DropdownMenuItem onClick={() => onRead(book)}>
                <BookOpenText className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Read
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onClick={() => onOpen(book)}>
              <BookOpen className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Book details
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={autoGrab} disabled={!book.libraryId}>
              <Zap className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Auto-grab best release
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => onSearch(book)} disabled={!book.libraryId}>
              <Search className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Search releases
            </DropdownMenuItem>
            {isAdmin && otherLibs.length > 0 && (
              <DropdownMenuSub>
                <DropdownMenuSubTrigger>
                  <FolderInput className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Move to library
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent className="rounded-xl">
                  {otherLibs.map(l => (
                    <DropdownMenuItem key={l.id} onClick={() => move(l)}>{l.name}</DropdownMenuItem>
                  ))}
                </DropdownMenuSubContent>
              </DropdownMenuSub>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <Pencil className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Metadata
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="rounded-xl">
                {isAdmin && (
                  <DropdownMenuItem onClick={() => onEdit(book)}>
                    <Pencil className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Edit metadata
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem onClick={refreshMeta}>
                  <RefreshCw className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Refresh metadata
                </DropdownMenuItem>
                <DropdownMenuItem onClick={regenCover}>
                  <Image className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Regenerate cover
                </DropdownMenuItem>
              </DropdownMenuSubContent>
            </DropdownMenuSub>
            <DropdownMenuSeparator />
            <DropdownMenuSub>
              <DropdownMenuSubTrigger className="text-want data-[state=open]:text-want">
                <Trash2 className="mr-2 h-3.5 w-3.5" /> Remove
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="rounded-xl">
                {removeModesFor(isAdmin).map(m => (
                  <DropdownMenuItem key={m.mode} disabled={!book.libraryId} title={m.desc}
                    onClick={() => remove(m.mode)} className="text-want">{m.label}</DropdownMenuItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {/* the text under the cover navigates piecewise: title → the book,
          author → their page, series → its page. While a selection is
          active, everything falls back to toggling — same as the cover. */}
      <div>
        <button type="button"
          className="block text-left text-[12.5px] font-semibold leading-tight hover:text-brass hover:underline"
          onClick={() => (selectionActive ? onToggleSelect(book) : onOpen(book))}>
          {book.title}
        </button>
        <button type="button"
          className="mt-0.5 block text-left text-[11.5px] text-muted-foreground hover:text-brass hover:underline"
          onClick={() => (selectionActive ? onToggleSelect(book) : onOpenAuthor(book))}>
          {book.author}
        </button>
        {seriesTag && book.seriesName && (
          <button type="button" className="mt-1 block text-left transition-opacity hover:opacity-75"
            aria-label={`Open series ${book.seriesName}`}
            onClick={() => (selectionActive ? onToggleSelect(book) : onOpenSeries(book))}>
            <Tag kind="dim">{book.seriesName}{book.seriesNum ? ` #${book.seriesNum}` : ""}</Tag>
          </button>
        )}
      </div>
    </div>
  )
}

function StackLayer({ book, offset }: { book: ApiBook; offset: number }) {
  const [c1, c2] = hashColors(book.title)
  const [imgOk, setImgOk] = useState(true)
  return (
    <div
      className="absolute inset-0 overflow-hidden rounded-[8px_12px_12px_8px] shadow-[0_4px_12px_rgba(0,0,0,0.5)]"
      style={{
        background: `linear-gradient(155deg, ${c1}, ${c2})`,
        transform: `translate(${offset}px, ${-offset}px)`,
        filter: `brightness(${1 - offset * 0.028})`,
      }}
    >
      {imgOk && (
        <img src={coverUrl(book.id)} alt="" loading="lazy" onError={() => setImgOk(false)}
          className="h-full w-full object-cover" />
      )}
    </div>
  )
}

// A deck of cards: the next books peek out clearly behind the lead cover,
// stepped up-and-right, real covers at full strength.
function SeriesStack({ books, onExpand, onOpenSeries, onOpenAuthor }: {
  books: ApiBook[]
  onExpand: () => void
  onOpenSeries: () => void
  onOpenAuthor: () => void
}) {
  const lead = books[0]
  const [c1, c2] = hashColors(lead.title)
  return (
    <div className="flex cursor-pointer flex-col gap-2" role="button" tabIndex={0}
      onClick={onExpand} onKeyDown={e => { if (e.key === "Enter") onExpand() }}>
      <div className="relative mr-[16px] mt-[16px] transition-transform hover:-translate-y-0.5">
        {books[2] && <StackLayer book={books[2]} offset={14} />}
        {books[1] && <StackLayer book={books[1]} offset={7} />}
        <div className="relative">
          <Cover c1={c1} c2={c2} title={lead.title} author={lead.author}
            src={coverUrl(lead.id)} />
          <span className="font-label absolute right-1.5 top-1.5 z-10 flex items-center gap-1 rounded-lg bg-black/70 px-1.5 py-0.5 text-[9.5px] font-bold text-white/90 backdrop-blur-xs">
            <Layers className="h-3 w-3" /> {books.length}
          </span>
        </div>
      </div>
      {/* the cover expands the deck in place; the series name goes to the
          series page and the author to theirs */}
      <div>
        <button type="button"
          className="block text-left text-[12.5px] font-semibold leading-tight hover:text-brass hover:underline"
          onClick={e => { e.stopPropagation(); onOpenSeries() }}>
          {lead.seriesName}
        </button>
        <div className="mt-0.5 text-[11.5px] text-muted-foreground">
          <button type="button" className="hover:text-brass hover:underline"
            onClick={e => { e.stopPropagation(); onOpenAuthor() }}>
            {lead.author}
          </button>
          {" · "}{books.length} books
        </div>
      </div>
    </div>
  )
}

export type { ScopeFilter } from "@/components/FilterRows"

export function LibraryView({ library, scope, libraries, initialFilters, scopeTitle, onOpenBook, onOpenAuthor, onOpenSeries, onRead, onSelectLibrary }: {
  library: ApiLibrary | null
  scope: Scope
  libraries: ApiLibrary[]
  initialFilters?: ScopeFilter[]
  scopeTitle?: string
  onOpenBook: (b: ApiBook) => void
  onOpenAuthor: (b: ApiBook) => void
  onOpenSeries: (b: ApiBook) => void
  onRead: (b: ApiBook) => void
  onSelectLibrary?: (l: ApiLibrary | null) => void
}) {
  const booksQuery = useApi(() => api.books(library ? { libraryId: library.id } : {}), 5_000)
  // The Library is only what the user curated: catalog-only bibliography
  // books (no library membership) live on author/series pages, not here.
  const allBooks = useMemo(() => (booksQuery.data?.books ?? []).filter(b => b.libraryId), [booksQuery.data])

  const [stack, setStack] = useState(true)
  const [missingOnly, setMissingOnly] = useState(false)
  const [sort, setSort] = useState<SortKey>("added")
  const [sortAsc, setSortAsc] = useState(SORT_DEFAULT_ASC.added)
  // picking a new sort starts from its natural direction; picking the active
  // one again flips it
  const pickSort = (v: SortKey) => {
    if (sort === v) setSortAsc(a => !a)
    else { setSort(v); setSortAsc(SORT_DEFAULT_ASC[v]) }
  }
  const [query, setQuery] = useState("")
  const [selection, setSelection] = useState<Map<string, ApiBook>>(new Map())
  // composable filters: rows of field · is / is not · value. Rows with an
  // empty value are drafts and don't apply. "is" values in one field OR
  // together, "is not" always excludes, fields AND.
  const [filters, setFilters] = useState<ScopeFilter[]>(initialFilters ?? [])
  const [filterOpen, setFilterOpen] = useState(false)
  const active = useMemo(() => filters.filter(f => f.value.trim() !== ""), [filters])
  const [expanded, setExpanded] = useState<Set<number>>(new Set())
  const [editBook, setEditBook] = useState<ApiBook | null>(null)
  const [searchBook, setSearchBook] = useState<ApiBook | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [bust, setBust] = useState(0)

  // one-click "find everything I'm missing": monitored books without a file
  // plus cutoff-unmet upgrades, queued in the background
  const searchAllMissing = async () => {
    try {
      const r = await api.searchLibrary(library?.id ?? 0)
      toast.success(r.queued > 0
        ? `Searching ${r.queued} book${r.queued === 1 ? "" : "s"} (missing + upgrades) — watch Activity`
        : "Nothing to search — every monitored book is on the shelf at cutoff")
    } catch (e) {
      toast.error(`Search failed: ${e instanceof Error ? e.message : e}`)
    }
  }

  const bookStatus = (b: ApiBook) => (b.filePath ? "On shelf" : b.monitored ? "Missing" : "Unmonitored")

  const fieldValue = (b: ApiBook, field: string): string[] => {
    switch (field) {
      case "Author": return [b.author]
      case "Series": return b.seriesName ? [b.seriesName] : []
      case "Genre": return b.genres ?? []
      case "Format": return b.fileFormat ? [b.fileFormat.toUpperCase()] : []
      case "Status": return [bookStatus(b)]
      case "Language": return b.language ? [b.language] : []
      default: return []
    }
  }
  const removeFilter = (idx: number) => setFilters(prev => prev.filter((_, i) => i !== idx))

  // upgrade scope = the server's cutoff-unmet list (file present, below the
  // profile's cutoff format)
  const wantedQuery = useApi(() => acquisition.wanted(), 10_000)
  const upgradeKeys = useMemo(() => {
    const set = new Set<string>()
    for (const b of wantedQuery.data?.cutoffUnmet ?? []) set.add(`${b.id}-${b.libraryId}`)
    return set
  }, [wantedQuery.data])

  const books = useMemo(() => {
    let list = allBooks
    if (scope === "missing") list = list.filter(b => !b.filePath && b.monitored)
    if (scope === "recent") list = [...list].sort((a, b) => byAdded(b, a)).slice(0, 60)
    if (scope === "upgrade") list = list.filter(b => upgradeKeys.has(`${b.id}-${b.libraryId}`))
    if (missingOnly) list = list.filter(b => !b.filePath && b.monitored)
    const q = query.trim().toLowerCase()
    if (q) {
      list = list.filter(b =>
        b.title.toLowerCase().includes(q) ||
        b.author.toLowerCase().includes(q) ||
        (b.seriesName?.toLowerCase().includes(q) ?? false))
    }
    // per field: "is" values OR together, "is not" values always exclude.
    // Matching is fuzzy so the grid narrows live while a value is typed.
    const byField = new Map<string, { is: string[]; not: string[] }>()
    for (const f of active) {
      const g = byField.get(f.field) ?? { is: [], not: [] }
      g[f.op === "not" ? "not" : "is"].push(f.value)
      byField.set(f.field, g)
    }
    for (const [field, g] of byField) {
      list = list.filter(b => {
        const vals = fieldValue(b, field)
        if (g.is.length > 0 && !vals.some(v => g.is.some(w => fuzzyMatch(w, v)))) return false
        if (vals.some(v => g.not.some(w => fuzzyMatch(w, v)))) return false
        return true
      })
    }
    const cmp = SORT_CMP[sort]
    const dir = sortAsc ? 1 : -1
    return [...list].sort((a, b) => dir * cmp(a, b))
  }, [allBooks, scope, active, sort, sortAsc, query, upgradeKeys, missingOnly])

  // group into stacks (series with 2+ books collapse unless expanded)
  const cells = useMemo(() => {
    if (!stack) return books.map(b => ({ kind: "book" as const, book: b }))
    const bySeries = new Map<number, ApiBook[]>()
    for (const b of books) {
      if (b.seriesId) {
        const list = bySeries.get(b.seriesId) ?? []
        list.push(b)
        bySeries.set(b.seriesId, list)
      }
    }
    const out: ({ kind: "book"; book: ApiBook } | { kind: "stack"; seriesId: number; books: ApiBook[] })[] = []
    const emitted = new Set<number>()
    for (const b of books) {
      if (b.seriesId && bySeries.get(b.seriesId)!.length > 1 && !expanded.has(b.seriesId)) {
        if (!emitted.has(b.seriesId)) {
          emitted.add(b.seriesId)
          const list = [...bySeries.get(b.seriesId)!].sort((x, y) => (x.seriesNum ?? 0) - (y.seriesNum ?? 0))
          out.push({ kind: "stack", seriesId: b.seriesId, books: list })
        }
      } else {
        out.push({ kind: "book", book: b })
      }
    }
    return out
  }, [books, stack, expanded])

  const onShelf = allBooks.filter(b => b.filePath).length
  const seriesCount = useMemo(() => new Set(allBooks.filter(b => b.seriesId).map(b => b.seriesId)).size, [allBooks])
  const sizeMB = allBooks.reduce((sum, b) => sum + (b.fileSize ?? 0), 0) / 1e6
  const title = scopeTitle ?? (library && scope === "all" ? library.name : SCOPE_TITLES[scope])
  const changed = () => { setBust(Date.now()); booksQuery.reload() }

  return (
    <section>
      <Folio
        title={title}
        meta={booksQuery.loading ? "loading…" :
          `${allBooks.length} book${allBooks.length === 1 ? "" : "s"}${seriesCount > 0 ? ` · ${seriesCount} series` : ""} · ${onShelf} on shelf · ${sizeMB > 1000 ? (sizeMB / 1000).toFixed(1) + " gb" : Math.round(sizeMB) + " mb"}`}
        end={
          <div className="flex flex-wrap items-center gap-2">
            {/* free-text filter over title / author / series */}
            <div className="relative w-full max-w-[220px]">
              <Search className="absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
              <Input value={query} onChange={e => setQuery(e.target.value)}
                placeholder="Search library…" className="h-8 pl-9 text-[13px]" />
            </div>
            {/* search everything wanted: missing + cutoff-unmet, monitored only */}
            <button
              onClick={searchAllMissing}
              title="Search all monitored missing books (and pending upgrades)"
              className="flex h-8 items-center gap-1.5 rounded-[10px] border px-2.5 text-[12.5px] font-medium text-muted-foreground hover:text-foreground"
            >
              <Search className="h-3.5 w-3.5" /> Search missing
            </button>

            {/* import a file for any book — even one not in Booky yet */}
            <button
              onClick={() => setImportOpen(true)}
              title="Import a book file you already have — the book doesn't need to be in Booky yet"
              className="flex h-8 w-8 items-center justify-center rounded-[10px] border text-muted-foreground hover:text-foreground"
            >
              <FolderInput className="h-3.5 w-3.5" />
            </button>

            {/* sort: the field picker and a direction toggle share one pill.
                The arrow always points the way the list is running, and one
                click flips it — as does re-picking the active field. */}
            <div className="flex h-8 items-center rounded-[10px] border text-muted-foreground">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button className="flex h-full items-center rounded-l-[9px] px-2.5 text-[12.5px] font-medium hover:text-foreground">
                    {SORT_LABELS[sort]}
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="rounded-xl">
                  <DropdownMenuLabel className="mono-label text-faint">Sort by</DropdownMenuLabel>
                  {(Object.keys(SORT_LABELS) as SortKey[]).map(v => (
                    <DropdownMenuCheckboxItem key={v} checked={sort === v} onCheckedChange={() => pickSort(v)}>
                      {SORT_LABELS[v]}
                      {sort === v && (sortAsc
                        ? <ArrowUp className="ml-3 h-3 w-3 text-muted-foreground" />
                        : <ArrowDown className="ml-3 h-3 w-3 text-muted-foreground" />)}
                    </DropdownMenuCheckboxItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
              <button onClick={() => setSortAsc(a => !a)}
                aria-label={sortAsc ? "Sorted ascending — click for descending" : "Sorted descending — click for ascending"}
                title={sort === "added"
                  ? (sortAsc ? "Oldest first" : "Newest first")
                  : (sortAsc ? "Ascending" : "Descending")}
                className="flex h-full w-8 items-center justify-center rounded-r-[9px] border-l hover:text-foreground">
                {sortAsc
                  ? <ArrowUpNarrowWide className="h-3.5 w-3.5" />
                  : <ArrowDownWideNarrow className="h-3.5 w-3.5" />}
              </button>
            </div>

            {/* filter builder: rows of field · is / is not · value; chips show below */}
            <Popover open={filterOpen} onOpenChange={open => {
              setFilterOpen(open)
              if (open && filters.length === 0) setFilters([{ field: "Author", op: "is", value: "" }])
              if (!open) setFilters(prev => prev.filter(f => f.value.trim() !== ""))
            }}>
              <PopoverTrigger asChild>
                <button title="Filters"
                  className={cn(
                    "flex h-8 w-8 items-center justify-center rounded-[10px] border text-muted-foreground hover:text-foreground",
                    active.length > 0 && "border-brass/50 bg-brass/15 text-brass"
                  )}>
                  <Filter className="h-3.5 w-3.5" />
                </button>
              </PopoverTrigger>
              <PopoverContent align="end" className="w-[400px] rounded-xl p-3">
                <div className="mono-label mb-2.5 text-faint">Filter by</div>
                <FilterRows rows={filters} onChange={setFilters} books={allBooks} />
                {active.length > 0 && (
                  <div className="mt-2.5 flex justify-end border-t pt-2.5">
                    <Button variant="ghost" className="h-7 px-2 text-muted-foreground"
                      onClick={() => { setFilters([]); setFilterOpen(false) }}>
                      Clear all filters
                    </Button>
                  </div>
                )}
              </PopoverContent>
            </Popover>

            {/* missing-only toggle: hide everything already on the shelf */}
            {scope !== "missing" && (
              <button
                onClick={() => setMissingOnly(m => !m)}
                title={missingOnly ? "Showing only missing books — click to show everything" : "Show only missing books (monitored, no file yet)"}
                className={cn(
                  "flex h-8 w-8 items-center justify-center rounded-[10px] border text-muted-foreground hover:text-foreground",
                  missingOnly && "border-brass/50 bg-brass/15 text-brass"
                )}
              >
                <BookDashed className="h-3.5 w-3.5" />
              </button>
            )}

            {/* stack toggle: icon only */}
            <button
              onClick={() => { setStack(s => !s); setExpanded(new Set()) }}
              title={stack ? "Series stacked — click to expand all" : "Series expanded — click to stack"}
              className={cn(
                "flex h-8 w-8 items-center justify-center rounded-[10px] border text-muted-foreground hover:text-foreground",
                stack && "border-brass/50 bg-brass/15 text-brass"
              )}
            >
              <Layers className="h-3.5 w-3.5" />
            </button>
          </div>
        }
      />

      {/* phones have no sidebar, so the library list lives here: a chip row
          for All + each library (and the smart scopes' reachable too) */}
      {onSelectLibrary && libraries.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-1.5 md:hidden">
          <button onClick={() => onSelectLibrary(null)}
            className={cn(
              "rounded-lg border px-2.5 py-1 text-[12px] font-medium",
              !library && scope === "all" && !scopeTitle
                ? "border-brass/50 bg-brass/15 text-brass"
                : "text-muted-foreground"
            )}>
            All libraries
          </button>
          {libraries.map(l => (
            <button key={l.id} onClick={() => onSelectLibrary(l)}
              className={cn(
                "rounded-lg border px-2.5 py-1 text-[12px] font-medium",
                library?.id === l.id
                  ? "border-brass/50 bg-brass/15 text-brass"
                  : "text-muted-foreground"
              )}>
              {l.name}
            </button>
          ))}
        </div>
      )}

      {active.length > 0 && (
        <div className="mb-5 flex flex-wrap items-center gap-1.5">
          {filters.map((f, i) => (
            f.value.trim() !== "" && (
              <button key={i} onClick={() => removeFilter(i)}
                className="group flex items-center gap-1.5 rounded-lg border border-brass/40 bg-brass/10 px-2.5 py-1 text-[12px] text-brass hover:border-brass"
                title="Remove filter">
                <span className="mono-label text-[9px] opacity-70">{f.field} {f.op === "not" ? "is not" : "is"}</span>
                {f.value}
                <span className="opacity-60 group-hover:opacity-100">×</span>
              </button>
            )
          ))}
        </div>
      )}

      {booksQuery.error && (
        <div className="rounded-r-lg border-l-[3px] border-want bg-want/10 px-4 py-3 text-[13px]">
          Couldn't load the library: {booksQuery.error}
        </div>
      )}
      {!booksQuery.loading && !booksQuery.error && allBooks.length === 0 && (
        <div className="max-w-[52ch] text-[13.5px] leading-relaxed text-muted-foreground">
          <p className="font-book mb-2 text-xl font-bold text-foreground">The shelf is empty.</p>
          <p>Search for a book up top to add one, or scan an existing folder from the ⋯ menu on a library in the sidebar.</p>
        </div>
      )}
      {!booksQuery.loading && allBooks.length > 0 && books.length === 0 && scope === "upgrade" && (
        <p className="max-w-[52ch] text-[13.5px] text-muted-foreground">
          No upgrades pending — every file meets its profile's cutoff format.
        </p>
      )}

      <div className="grid grid-cols-[repeat(auto-fill,minmax(102px,1fr))] gap-x-3 gap-y-5 sm:grid-cols-[repeat(auto-fill,minmax(134px,1fr))] sm:gap-x-4 sm:gap-y-6">
        {cells.map(cell =>
          cell.kind === "stack" ? (
            <SeriesStack key={`s${cell.seriesId}`} books={cell.books}
              onExpand={() => setExpanded(prev => new Set(prev).add(cell.seriesId))}
              onOpenSeries={() => onOpenSeries(cell.books[0])}
              onOpenAuthor={() => onOpenAuthor(cell.books[0])} />
          ) : (
            <BookCell key={`${cell.book.id}-${cell.book.libraryId}`} book={cell.book}
              libraries={libraries} bust={bust}
              seriesTag={!!cell.book.seriesId}
              onOpenAuthor={onOpenAuthor}
              onOpenSeries={onOpenSeries}
              selected={selection.has(`${cell.book.id}-${cell.book.libraryId}`)}
              selectionActive={selection.size > 0}
              onToggleSelect={b => setSelection(prev => {
                const next = new Map(prev)
                const k = `${b.id}-${b.libraryId}`
                if (next.has(k)) next.delete(k); else next.set(k, b)
                return next
              })}
              onOpen={onOpenBook} onRead={onRead} onEdit={setEditBook} onSearch={setSearchBook} onChanged={changed} />
          )
        )}
      </div>

      <SelectionBar
        books={[...selection.values()]}
        libraries={libraries}
        onClear={() => setSelection(new Map())}
        onChanged={changed}
      />

      {editBook && (
        <EditMetadataDialog open onOpenChange={v => { if (!v) setEditBook(null) }}
          book={editBook} onSaved={() => { setEditBook(null); changed() }} />
      )}
      <ReleasesDialog book={searchBook} open={!!searchBook}
        onOpenChange={v => { if (!v) setSearchBook(null) }}
        onGrabbed={() => { toast("Watch Activity for progress"); changed() }} />
      <ImportBookDialog open={importOpen} onOpenChange={setImportOpen}
        libraries={libraries} defaultLibraryId={library?.id} onImported={changed} />
    </section>
  )
}
