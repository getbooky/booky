import { useMemo } from "react"
import { MiniCover, hashColors } from "@/components/Cover"
import { Tag, Folio } from "@/components/bits"
import { watchers, coverUrl } from "@/api"
import type { ApiBook, ApiLibrary } from "@/api"
import { useApi } from "@/hooks/use-api"

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]
const MONTH_NAMES = ["January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December"]

// Monitored books with release dates, grouped by month — dates are refreshed
// weekly in the background (they slip), and each book is searched on release
// day with a short taper after.
export function CalendarView({ libraries, onOpenBook }: {
  libraries: ApiLibrary[]
  onOpenBook?: (b: ApiBook) => void
}) {
  const { data, loading } = useApi(() => watchers.calendar(), 30_000)
  const books = useMemo(() => data?.books ?? [], [data])
  const libName = (id?: number) => libraries.find(l => l.id === id)?.name ?? ""

  const months = useMemo(() => {
    const byMonth = new Map<string, ApiBook[]>()
    for (const b of books) {
      const key = b.releaseDate!.slice(0, 7) // YYYY-MM
      byMonth.set(key, [...(byMonth.get(key) ?? []), b])
    }
    return [...byMonth.entries()].map(([key, entries]) => {
      const [y, m] = key.split("-").map(Number)
      return { name: `${MONTH_NAMES[m - 1]} ${y}`, entries }
    })
  }, [books])

  return (
    <section>
      <Folio title="Calendar" meta={loading ? "loading…" : "Upcoming releases · monitored authors & series"} />
      {!loading && months.length === 0 && (
        <p className="max-w-[56ch] text-[13.5px] leading-relaxed text-muted-foreground">
          No release dates on the horizon. Monitored books with future release dates appear here —
          their dates refresh weekly, and each gets searched automatically on release day.
        </p>
      )}
      {months.map(m => (
        <div key={m.name} className="mb-6">
          <h2 className="font-book mb-1 text-xl font-bold">{m.name}</h2>
          <div className="border-t">
            {m.entries.map(b => {
              const [c1, c2] = hashColors(b.title)
              const date = new Date(b.releaseDate + "T00:00:00Z")
              return (
                <div key={`${b.id}-${b.libraryId}`} role="button" tabIndex={0}
                  onClick={() => onOpenBook?.(b)}
                  onKeyDown={e => { if (e.key === "Enter") onOpenBook?.(b) }}
                  className="grid cursor-pointer grid-cols-[60px_40px_1fr_auto] items-center gap-4 border-b border-linesoft px-1 py-3 hover:bg-surface">
                  <div className="text-center">
                    <div className="font-book text-2xl font-bold leading-none">{date.getUTCDate()}</div>
                    <div className="font-label mt-1 text-[9.5px] uppercase tracking-[0.12em] text-faint">{WEEKDAYS[date.getUTCDay()]}</div>
                  </div>
                  <MiniCover c1={c1} c2={c2} src={coverUrl(b.id)} />
                  <div>
                    <div className="text-[13.5px] font-semibold">
                      {b.title}
                      {b.seriesName && (
                        <Tag kind="dim" className="ml-1.5">{b.seriesName}{b.seriesNum ? ` #${b.seriesNum}` : ""}</Tag>
                      )}
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                      {b.author}
                      {b.libraryId ? <Tag kind="dim">{libName(b.libraryId)}</Tag> : null}
                    </div>
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      ))}
    </section>
  )
}
