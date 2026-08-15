import { useState } from "react"
import { api, coverUrl } from "@/api"
import type { ApiLibrary, ApiSeries } from "@/api"
import { useApi } from "@/hooks/use-api"
import { AddToLibraryButton } from "@/components/AddToLibrary"
import { hashColors } from "@/components/Cover"
import { Tag, Folio, Chips } from "@/components/bits"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Input } from "@/components/ui/input"
import { Search } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

// CoverFan stacks the series' first few covers like books leaning on a
// shelf; gradient placeholders stand in for missing images.
function CoverFan({ s }: { s: ApiSeries }) {
  const ids = (s.coverBookIds ?? []).slice(0, 3)
  const slots = ids.length > 0 ? ids : [0]
  return (
    <div className="relative h-[124px]" style={{ width: 76 + (slots.length - 1) * 34 }}>
      {slots.map((id, i) => {
        const [c1, c2] = hashColors(`${s.name}#${i}`)
        return (
          <div
            key={`${id}-${i}`}
            className="cover-shadow absolute top-1/2 aspect-2/3 w-[76px] overflow-hidden rounded-[4px_8px_8px_4px] border border-black/20"
            style={{
              left: i * 34,
              zIndex: slots.length - i,
              transform: `translateY(-50%) rotate(${(i - (slots.length - 1) / 2) * 4}deg)`,
              background: `linear-gradient(155deg, ${c1}, ${c2})`,
            }}
          >
            {id !== 0 && (
              <img src={coverUrl(id)} alt="" loading="lazy"
                onError={e => { (e.target as HTMLImageElement).style.display = "none" }}
                className="absolute inset-0 h-full w-full object-cover" />
            )}
          </div>
        )
      })}
    </div>
  )
}

// Progress paints one segment per book for short series and a single filled
// bar once segments would turn to dust.
function Progress({ s }: { s: ApiSeries }) {
  if (s.total === 0) return null
  if (s.total <= 14) {
    return (
      <div className="flex gap-[3px]">
        {Array.from({ length: s.total }, (_, i) => (
          <span key={i} className={cn("h-1.5 flex-1 rounded-[2px]", i < s.onShelf ? "bg-good" : "bg-surface3")} />
        ))}
      </div>
    )
  }
  return (
    <div className="h-1.5 overflow-hidden rounded-[2px] bg-surface3">
      <div className="h-full rounded-[2px] bg-good" style={{ width: `${(s.onShelf / s.total) * 100}%` }} />
    </div>
  )
}

export function SeriesView({ onOpenSeries }: {
  onOpenSeries: (s: ApiSeries) => void
}) {
  const [filter, setFilter] = useState<"all" | "monitored" | "incomplete">("all")
  const [sort, setSort] = useState("name")
  const [query, setQuery] = useState("")
  const { data, loading, error, reload } = useApi(() => api.series(), 10_000)
  const series = data?.series ?? []
  const monitored = series.filter(s => s.monitored).length
  const q = query.trim().toLowerCase()

  const rows = series
    .filter(s => {
      if (q && !s.name.toLowerCase().includes(q) && !s.author.toLowerCase().includes(q)) return false
      if (filter === "monitored") return s.monitored
      if (filter === "incomplete") return s.onShelf < s.total
      return true
    })
    .sort((a, b) => {
      if (sort === "author") return a.author.localeCompare(b.author) || a.name.localeCompare(b.name)
      if (sort === "completion") return (a.onShelf / Math.max(a.total, 1)) - (b.onShelf / Math.max(b.total, 1))
      return a.name.localeCompare(b.name)
    })

  // Shelving names its library. The list is already scoped to whoever is
  // asking, so a user with one library is never asked to choose.
  const libsQuery = useApi(() => api.libraries())
  const libraries = libsQuery.data?.libraries ?? []
  const shelve = async (s: ApiSeries, library: ApiLibrary) => {
    try {
      const r = await api.addSeriesToLibrary(s.id, library.id)
      toast.success(r.queued > 0
        ? `${s.name} added to ${library.name} — searching for ${r.queued} missing ${r.queued === 1 ? "book" : "books"}`
        : `${s.name} added to ${library.name}`)
      reload()
    } catch (e) {
      toast.error(`Couldn't add ${s.name}: ${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <section>
      <Folio
        title="Series"
        meta={loading ? "loading…" : `${series.length} series · ${monitored} monitored`}
        end={
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative w-full max-w-[220px]">
              <Search className="absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
              <Input value={query} onChange={e => setQuery(e.target.value)}
                placeholder="Search series…" className="h-8 pl-9 text-[13px]" />
            </div>
            <Select value={sort} onValueChange={setSort}>
              <SelectTrigger className="h-8 w-[120px] rounded-[10px] text-[12.5px]"><SelectValue /></SelectTrigger>
              <SelectContent className="rounded-xl">
                <SelectItem value="name">Name</SelectItem>
                <SelectItem value="author">Author</SelectItem>
                <SelectItem value="completion">Least complete</SelectItem>
              </SelectContent>
            </Select>
            <Chips options={[{ v: "all", label: "All" }, { v: "monitored", label: "Monitored" }, { v: "incomplete", label: "Incomplete" }]} value={filter} onChange={setFilter} />
          </div>
        }
      />
      {error && <div className="border-l-[3px] border-want bg-want/10 px-4 py-3 text-[13px]">Couldn't load series: {error}</div>}
      {!loading && !error && series.length === 0 && (
        <p className="max-w-[52ch] text-[13.5px] text-muted-foreground">
          No series yet — they appear automatically when series books are added.
        </p>
      )}
      <div className="grid grid-cols-[repeat(auto-fill,minmax(168px,1fr))] gap-3 sm:grid-cols-[repeat(auto-fill,minmax(250px,1fr))] sm:gap-4">
        {rows.map(s => {
          const complete = s.total > 0 && s.onShelf === s.total
          return (
            <div key={s.id}
              className="group rounded-2xl border border-linesoft bg-surface p-3 transition-colors hover:border-brass/40 sm:p-4">
              <button className="block w-full text-left" onClick={() => onOpenSeries(s)} aria-label={`Open ${s.name}`}>
                <div className="flex justify-center pb-1 pt-2">
                  <CoverFan s={s} />
                </div>
                <div className="font-book mt-2 truncate text-[16.5px] font-bold group-hover:text-brass">{s.name}</div>
                <div className="truncate text-xs text-muted-foreground">{s.author}</div>
              </button>
              <div className="mt-3">
                <Progress s={s} />
                <div className="mt-2 flex items-center justify-between gap-2">
                  <span className="mono-label text-muted-foreground">{s.onShelf} of {s.total} on shelf</span>
                  <span className="flex items-center gap-2">
                    {complete && <Tag kind="good" className="hidden sm:inline-block">Complete</Tag>}
                    {!complete && (
                      <span onClick={e => e.stopPropagation()}>
                        <AddToLibraryButton libraries={libraries} label="Add" className="h-8"
                          onPick={library => shelve(s, library)} />
                      </span>
                    )}
                  </span>
                </div>
              </div>
            </div>
          )
        })}
      </div>
    </section>
  )
}
