import { useMemo, useRef } from "react"
import { ChevronLeft, ChevronRight } from "lucide-react"
import { api, coverUrl, watchers } from "@/api"
import type { ApiBook, ApiLibrary, ApiTag } from "@/api"
import { useTags } from "@/components/TagPills"
import { useAccess } from "@/lib/access"
import { Cover, hashColors } from "@/components/Cover"
import { bookRibbon } from "@/views/Library"
import { LIB_COLORS } from "@/components/Sidebar"
import { useApi } from "@/hooks/use-api"
import { cn } from "@/lib/utils"

// one copy per book: the all-libraries listing repeats a book for every
// library holding it, and a shelf row should show it once
function dedupe(rows: ApiBook[]): ApiBook[] {
  const byId = new Map<number, ApiBook>()
  for (const b of rows) {
    const seen = byId.get(b.id)
    if (!seen || (!seen.filePath && b.filePath)) byId.set(b.id, b)
  }
  return [...byId.values()]
}

const newestFirst = (a: ApiBook, b: ApiBook) =>
  (b.addedAt ?? "").localeCompare(a.addedAt ?? "") || b.id - a.id

// ShelfRow is a horizontally scrolling row of covers under a serif heading,
// with arrow buttons on wide screens and swipe on touch.
function ShelfRow({ title, meta, books, onOpenBook, onSeeAll, dot }: {
  title: string
  meta?: string
  books: ApiBook[]
  onOpenBook: (b: ApiBook) => void
  onSeeAll?: () => void
  dot?: string
}) {
  const scroller = useRef<HTMLDivElement>(null)
  const nudge = (dir: number) => scroller.current?.scrollBy({ left: dir * scroller.current.clientWidth * 0.8, behavior: "smooth" })
  if (books.length === 0) return null
  return (
    <section className="mb-9">
      <div className="mb-3 flex items-baseline gap-3">
        {dot && <span className="h-2 w-2 shrink-0 translate-y-[-2px] rounded-full" style={{ background: dot }} />}
        <h2 className="font-book text-[21px] font-bold tracking-tight">{title}</h2>
        {meta && <span className="mono-label text-faint">{meta}</span>}
        <div className="ml-auto flex items-center gap-1.5">
          {onSeeAll && (
            <button onClick={onSeeAll} className="mono-label mr-1 text-muted-foreground hover:text-brass">See all</button>
          )}
          <button aria-label="Scroll left" onClick={() => nudge(-1)}
            className="hidden h-7 w-7 items-center justify-center rounded-lg border border-linesoft text-muted-foreground hover:text-brass md:flex">
            <ChevronLeft className="h-4 w-4" />
          </button>
          <button aria-label="Scroll right" onClick={() => nudge(1)}
            className="hidden h-7 w-7 items-center justify-center rounded-lg border border-linesoft text-muted-foreground hover:text-brass md:flex">
            <ChevronRight className="h-4 w-4" />
          </button>
        </div>
      </div>
      <div ref={scroller} className="-mx-1 flex snap-x items-start gap-3.5 overflow-x-auto px-1 pb-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {books.map(b => {
          const [c1, c2] = hashColors(b.title)
          return (
            <button key={`${b.id}-${b.libraryId}`} onClick={() => onOpenBook(b)}
              className="group w-[112px] shrink-0 snap-start text-left sm:w-[128px]">
              <div className="transition-transform duration-200 group-hover:scale-[1.025]">
                <Cover c1={c1} c2={c2} title={b.title} author={b.author} src={coverUrl(b.id)} ribbon={bookRibbon(b)} />
              </div>
              <div className="mt-2 line-clamp-2 text-[12.5px] font-semibold leading-snug group-hover:text-brass">{b.title}</div>
              <div className="truncate text-[11.5px] text-muted-foreground">{b.author}</div>
            </button>
          )
        })}
      </div>
    </section>
  )
}

export function HomeView({ libraries, userName, onOpenBook, onOpenTag, onBrowse }: {
  libraries: ApiLibrary[]
  userName?: string
  onOpenBook: (b: ApiBook) => void
  onOpenTag: (t: ApiTag) => void
  onBrowse: (v: "library" | "calendar" | "wanted") => void
}) {
  const booksQuery = useApi(() => api.books(), 15_000)
  const calQuery = useApi(() => watchers.calendar(), 60_000)
  const rows = useMemo(() => (booksQuery.data?.books ?? []).filter(b => b.libraryId), [booksQuery.data])
  const all = useMemo(() => dedupe(rows), [rows])

  const recent = useMemo(() => all.filter(b => b.filePath).sort(newestFirst).slice(0, 24), [all])
  const wanted = useMemo(() => all.filter(b => !b.filePath && b.monitored).sort(newestFirst).slice(0, 24), [all])
  const upcoming = useMemo(() => dedupe(calQuery.data?.books ?? []).slice(0, 24), [calQuery.data])
  // one row per shared tag, so a household sees "Kids", "Book club"… at a
  // glance; personal tags stay out of it (that's what My shelf is for)
  const { tags } = useTags()
  const { personalTagId } = useAccess()
  const perTag = useMemo(() => [
    ...tags.filter(t => t.id === personalTagId),
    ...tags.filter(t => !t.personal),
  ].map(t => ({
    tag: t,
    color: LIB_COLORS[t.id % LIB_COLORS.length],
    books: all.filter(b => b.filePath && (b.tagIds ?? []).includes(t.id)).sort(newestFirst).slice(0, 24),
  })), [tags, all, personalTagId])

  const hour = new Date().getHours()
  const greeting = hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening"
  const onShelf = all.filter(b => b.filePath).length

  return (
    <div>
      <div className="mb-7 flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <h1 className="font-book text-[30px] font-bold tracking-tight">
          {greeting}{userName ? `, ${userName}` : ""}
        </h1>
        {!booksQuery.loading && (
          <span className="mono-label text-muted-foreground">
            {onShelf} on the shelf · {wanted.length} wanted</span>
        )}
        {!booksQuery.loading && libraries.length === 0 && (
          <span className="mono-label text-want">no library set up yet
          </span>
        )}
      </div>

      <ShelfRow title="Recently added" books={recent} onOpenBook={onOpenBook} onSeeAll={() => onBrowse("library")} />
      <ShelfRow title="Coming soon" meta="monitored releases" books={upcoming} onOpenBook={onOpenBook} onSeeAll={() => onBrowse("calendar")} />
      <ShelfRow title="Still wanted" books={wanted} onOpenBook={onOpenBook} onSeeAll={() => onBrowse("wanted")} />
      {perTag.map(p => (
        <ShelfRow key={p.tag.id} title={p.tag.id === personalTagId ? "My shelf" : p.tag.name} dot={p.color}
          meta={p.tag.id === personalTagId ? "just for you" : "shared"} books={p.books}
          onOpenBook={onOpenBook} onSeeAll={() => onOpenTag(p.tag)} />
      ))}

      {!booksQuery.loading && all.length === 0 && (
        <p className={cn("max-w-[56ch] text-[13.5px] leading-relaxed text-muted-foreground")}>
          Nothing on the shelf yet. Add a book from the search bar above, or point a watched list at a library in Settings.
        </p>
      )}
    </div>
  )
}
