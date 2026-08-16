import { useMemo, useState } from "react"
import { api, canRead, coverUrl } from "@/api"
import type { ApiAuthor, ApiBook, ApiLibrary } from "@/api"
import { MonitorSwitch } from "@/components/AddToLibrary"
import { useApi } from "@/hooks/use-api"
import { MiniCover, hashColors } from "@/components/Cover"
import { Tag, Fmt, Chips } from "@/components/bits"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { AuthorAvatar } from "@/components/AuthorAvatar"
import { BookOpenText, RefreshCw, Search, Trash2 } from "lucide-react"
import { useIsAdmin } from "@/lib/access"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { bookRibbon } from "@/views/Library"

const tabCls = "rounded-none border-b-2 border-transparent px-0.5 pb-2.5 pt-0 text-sm font-semibold data-[state=active]:border-brass data-[state=active]:bg-transparent data-[state=active]:shadow-none"

function BookRow({ book, libraries, onToggle, onPick, onOpen, onRead }: {
  book: ApiBook
  libraries: ApiLibrary[]
  onToggle: (b: ApiBook, v: boolean) => void
  onPick: (b: ApiBook, library: ApiLibrary) => void
  onOpen: (b: ApiBook) => void
  onRead: (b: ApiBook) => void
}) {
  const [c1, c2] = hashColors(book.title)
  // full date when known ("2025-09-29"), bare year for announced books
  const released = book.releaseDate || "—"
  return (
    <div className="grid grid-cols-[76px_40px_1fr_auto] items-center gap-4 border-b border-linesoft px-1 py-2.5">
      <span className="font-label text-right text-xs tabular-nums text-faint">{released}</span>
      <MiniCover c1={c1} c2={c2} src={coverUrl(book.id)} ribbon={bookRibbon(book)} />
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          {/* min-w-0 on both: flex items refuse to shrink below their
              nowrap text otherwise, and the overflow paints over the
              status column instead of ellipsizing */}
          <button className="min-w-0 truncate text-left text-[13.5px] font-semibold hover:text-brass" onClick={() => onOpen(book)}>
            {book.title}
          </button>
          {book.seriesName && (
            <Tag kind="dim" className="hidden min-w-0 max-w-[45%] shrink-2 truncate sm:inline-block">
              {book.seriesNum ? `#${book.seriesNum} of ${book.seriesName}` : book.seriesName}
            </Tag>
          )}
        </div>
        {(book.fileFormat || book.seriesName) && (
          <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            {/* phones: the series tag lives on its own line under the title
                instead of fighting it for width */}
            {book.seriesName && (
              <Tag kind="dim" className="min-w-0 max-w-full truncate sm:hidden">
                {book.seriesNum ? `#${book.seriesNum} of ${book.seriesName}` : book.seriesName}
              </Tag>
            )}
            {book.fileFormat && <Fmt>{book.fileFormat.toUpperCase()}</Fmt>}
          </div>
        )}
      </div>
      <div className="flex items-center justify-end gap-2.5">
        {canRead(book) && (
          <button aria-label={`Read ${book.title}`} title="Read" onClick={() => onRead(book)}
            className="flex h-7 w-7 items-center justify-center rounded-lg text-muted-foreground hover:bg-surface2 hover:text-brass">
            <BookOpenText className="h-4 w-4" />
          </button>
        )}
        {/* phones: the cover ribbon carries the status; the text pill
            would leave long titles no room at all */}
        {book.filePath
          ? <Tag kind="good" className="hidden sm:inline-block">On shelf</Tag>
          : <Tag kind={book.monitored ? "want" : "dim"} className="hidden sm:inline-block">{book.monitored ? "Missing" : book.libraryId ? "Not monitored" : "Not in library"}</Tag>}
        <MonitorSwitch book={book} libraries={libraries} onToggle={onToggle} onPick={onPick} />
      </div>
    </div>
  )
}

export function AuthorView({ author, onBack, onOpenBook, onRead, focusSeries }: {
  author: ApiAuthor
  onBack: () => void
  onOpenBook: (b: ApiBook) => void
  onRead: (b: ApiBook) => void
  focusSeries?: string
}) {
  const isAdmin = useIsAdmin()
  const { data, loading, reload } = useApi(() => api.books({ authorId: author.id }), 5_000)
  const allBooks = useMemo(() => data?.books ?? [], [data])
  const [tab, setTab] = useState<string>(focusSeries ? "series" : "books")
  // The bibliography syncs in automatically (catalog-only) — this filter
  // narrows the page to what the user actually curated.
  const [show, setShow] = useState<"all" | "library" | "monitored">("all")
  const [query, setQuery] = useState("")
  const books = useMemo(() => {
    const q = query.trim().toLowerCase()
    return allBooks.filter(b =>
      (show === "all" ? true : show === "library" ? !!b.libraryId : b.monitored)
      && (!q || b.title.toLowerCase().includes(q) || (b.seriesName?.toLowerCase().includes(q) ?? false))
    )
  }, [allBooks, show, query])

  const bySeries = useMemo(() => {
    const groups = new Map<string, ApiBook[]>()
    for (const b of books) {
      if (!b.seriesName) continue
      const list = groups.get(b.seriesName) ?? []
      list.push(b)
      groups.set(b.seriesName, list)
    }
    for (const list of groups.values()) list.sort((a, b) => (a.seriesNum ?? 0) - (b.seriesNum ?? 0))
    return groups
  }, [books])
  const standalone = books.filter(b => !b.seriesName)

  const libsQuery = useApi(() => api.libraries())
  const libraries = libsQuery.data?.libraries ?? []

  // Monitoring a catalog-only book is what puts it in a library. The switch
  // itself asks which one when there are several (MonitorSwitch) — toggle
  // only ever sees the direct cases.
  const monitorInto = async (b: ApiBook, library: ApiLibrary) => {
    try {
      await api.setBookMonitored(library.id, b.id, true)
      toast.success(`${b.title} added to ${library.name} — monitored`)
      reload()
    } catch (e) {
      toast.error(`Couldn't update: ${e instanceof Error ? e.message : e}`)
    }
  }
  const toggle = async (b: ApiBook, v: boolean) => {
    if (!b.libraryId) {
      if (!v) return // not shelved anywhere — nothing to unmonitor
      if (libraries.length === 0) {
        toast.error("Create a library first")
        return
      }
      await monitorInto(b, libraries[0])
      return
    }
    try {
      await api.setBookMonitored(b.libraryId, b.id, v)
      reload()
    } catch (e) {
      toast.error(`Couldn't update: ${e instanceof Error ? e.message : e}`)
    }
  }

  const onShelf = allBooks.filter(b => b.filePath).length
  const monitored = allBooks.filter(b => b.monitored).length
  const [refreshing, setRefreshing] = useState(false)
  const [bioOpen, setBioOpen] = useState(false)

  const removeAuthor = async (delMode: "catalog" | "files") => {
    const what = delMode === "files"
      ? `Remove ${author.name}, all their books, AND DELETE their files on disk (author folder included)?`
      : `Remove ${author.name} and all their books from Booky?\n\nFiles on disk are kept.`
    if (!window.confirm(what)) return
    try {
      const r = await api.deleteAuthor(author.id, delMode)
      toast(delMode === "files"
        ? `${author.name} removed — ${r.deletedFiles} file${r.deletedFiles === 1 ? "" : "s"} deleted`
        : `${author.name} removed — files on disk untouched`)
      onBack()
    } catch (e) {
      toast.error(`Couldn't remove: ${e instanceof Error ? e.message : e}`)
    }
  }

  // There is no monitor switch and no "what should the sync do" mode any
  // more. An author in a library is always tracked — nobody wants their
  // bibliography to stop updating — and what the sync finds always stays
  // catalog-only, listed below, until somebody shelves it.

  // The standard refresh button, same as books/libraries: re-syncs the
  // bibliography from the providers (the watcher also does this on its own —
  // new authors within seconds, monitored authors weekly).
  const refresh = async () => {
    setRefreshing(true)
    try {
      const r = await api.expandAuthor(author.id)
      toast.success(r.added > 0
        ? `${r.added} new book${r.added === 1 ? "" : "s"} in the bibliography`
        : "Up to date")
      reload()
    } catch (e) {
      toast.error(`Refresh failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <section>
      <button onClick={onBack} className="mono-label mb-5 flex items-center gap-1.5 text-muted-foreground hover:text-brass">
        ← Authors
      </button>
      <div className="flex flex-wrap items-start justify-between gap-5">
        <div className="flex min-w-0 items-center gap-4">
          <AuthorAvatar author={author} className="h-20 w-20" textClass="text-[26px]" />
          <div className="min-w-0">
            <span className="mono-label text-faint">Author</span>
            <h1 className="font-book text-[38px] font-bold leading-tight tracking-tight">{author.name}</h1>
          </div>
        </div>
        <TooltipProvider delayDuration={150}>
          <div className="flex shrink-0 items-center gap-2 pt-1.5">
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Search missing monitored books"
                  onClick={async () => {
                    try {
                      const r = await api.searchAuthor(author.id)
                      toast.success(r.queued > 0
                        ? `Searching ${r.queued} book${r.queued === 1 ? "" : "s"} (missing + upgrades) — watch Activity`
                        : "Nothing to search — every monitored book is on the shelf at cutoff")
                    } catch (e) {
                      toast.error(`Search failed: ${e instanceof Error ? e.message : e}`)
                    }
                  }}>
                  <Search className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Search monitored books that are missing or below their cutoff — never the whole bibliography</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Refresh author"
                  disabled={refreshing} onClick={refresh}>
                  <RefreshCw className={cn("h-4 w-4", refreshing && "animate-spin")} />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Refresh bibliography &amp; books</TooltipContent>
            </Tooltip>
            {isAdmin && (
              <DropdownMenu>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <DropdownMenuTrigger asChild>
                      <Button variant="outline" size="icon" className="h-9 w-9 text-want hover:border-want hover:text-want" aria-label="Remove author">
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </DropdownMenuTrigger>
                  </TooltipTrigger>
                  <TooltipContent>Remove author</TooltipContent>
                </Tooltip>
                <DropdownMenuContent align="end" className="rounded-xl">
                  <DropdownMenuItem onClick={() => removeAuthor("catalog")}>Remove — keep files on disk</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => removeAuthor("files")} className="text-want">Remove &amp; delete files (author folder too)</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </div>
        </TooltipProvider>
      </div>
      {author.bio && (
        <div className="mt-4 max-w-[72ch]">
          <p className={cn("text-[13.5px] leading-relaxed text-muted-foreground", !bioOpen && "line-clamp-3")}>
            {author.bio}
          </p>
          {author.bio.length > 280 && (
            <button onClick={() => setBioOpen(v => !v)} className="mono-label mt-1 text-faint hover:text-brass">
              {bioOpen ? "less" : "more"}
            </button>
          )}
        </div>
      )}
      <div className="mt-2.5 flex flex-wrap items-center gap-4">
        <span className="mono-label text-muted-foreground">
          {loading ? "loading…" : `${allBooks.length} books · ${new Set(allBooks.map(b => b.seriesName).filter(Boolean)).size} series · ${monitored} monitored · ${onShelf} on shelf`}
        </span>
        <Chips options={[
          { v: "all" as const, label: "All books" },
          { v: "library" as const, label: "In library" },
          { v: "monitored" as const, label: "Monitored" },
        ]} value={show} onChange={setShow} />
        <div className="relative w-full max-w-[260px] sm:ml-auto">
          <Search className="absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
          <Input value={query} onChange={e => setQuery(e.target.value)}
            placeholder="Search books…" aria-label="Search this author's books" className="h-8 pl-9 text-[13px]" />
        </div>
      </div>

      <Tabs value={tab} onValueChange={setTab} className="mt-6">
        <TabsList className="h-auto w-full justify-start gap-6 rounded-none border-b bg-transparent p-0">
          <TabsTrigger value="books" className={tabCls}>Books <span className="ml-1.5 text-faint">{books.length}</span></TabsTrigger>
          <TabsTrigger value="series" className={tabCls}>Series <span className="ml-1.5 text-faint">{bySeries.size}</span></TabsTrigger>
        </TabsList>

        <TabsContent value="books" className="mt-4">
          <div className="border-t">
            {books.map(b => <BookRow key={`${b.id}-${b.libraryId}`} book={b} libraries={libraries} onToggle={toggle} onPick={monitorInto} onOpen={onOpenBook} onRead={onRead} />)}
          </div>
          {books.length === 0 && !loading && (
            <p className="py-6 text-[13px] text-muted-foreground">
              {query.trim() ? <>No books match “{query.trim()}”.</> : "No books to show with the current filter."}
            </p>
          )}
        </TabsContent>

        <TabsContent value="series" className="mt-5">
          {bySeries.size === 0 && standalone.length === 0 && (
            <p className="text-[13px] text-muted-foreground">
              {query.trim() ? <>No series or books match “{query.trim()}”.</> : "No series for this author yet."}
            </p>
          )}
          {[...bySeries.entries()].map(([name, list]) => (
            <div key={name} className={cn("mb-7", focusSeries === name && "-ml-4 border-l-[3px] border-brass pl-4")}>
              <div className="mb-1 flex items-baseline gap-3.5">
                <h2 className="font-book text-xl font-bold">{name}</h2>
                <span className="mono-label text-faint">{list.length} books · in order</span>
              </div>
              <div className="border-t">
                {list.map(b => <BookRow key={`${b.id}-${b.libraryId}`} book={b} libraries={libraries} onToggle={toggle} onPick={monitorInto} onOpen={onOpenBook} onRead={onRead} />)}
              </div>
            </div>
          ))}
          {standalone.length > 0 && (
            <div className="mb-7">
              <div className="mb-1 flex items-baseline gap-3.5">
                <h2 className="font-book text-xl font-bold">Standalone</h2>
                <span className="mono-label text-faint">{standalone.length} books</span>
              </div>
              <div className="border-t">
                {standalone.map(b => <BookRow key={`${b.id}-${b.libraryId}`} book={b} libraries={libraries} onToggle={toggle} onPick={monitorInto} onOpen={onOpenBook} onRead={onRead} />)}
              </div>
            </div>
          )}
        </TabsContent>
      </Tabs>
    </section>
  )
}
