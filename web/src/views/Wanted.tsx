import { useMemo, useState } from "react"
import { MiniCover, hashColors } from "@/components/Cover"
import { Tag, Folio, Chips } from "@/components/bits"
import { Button } from "@/components/ui/button"
import { ReleasesDialog } from "@/components/ReleasesDialog"
import { acquisition, coverUrl } from "@/api"
import type { ApiBook, ApiLibrary } from "@/api"
import { useApi } from "@/hooks/use-api"
import { Search } from "lucide-react"
import { toast } from "sonner"

type Tab = "missing" | "cutoff"

const MONTHS_SHORT = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]

// "2027-10-10" → "Oct 10th 2027"
function fmtRelease(iso: string): string {
  const [y, m, d] = iso.split("-").map(Number)
  if (!y || !m || !d) return iso
  const suffix = d % 100 >= 11 && d % 100 <= 13 ? "th" : ["th", "st", "nd", "rd"][d % 10 < 4 ? d % 10 : 0]
  return `${MONTHS_SHORT[m - 1]} ${d}${suffix} ${y}`
}

// Wanted: missing = monitored books with no file; cutoff unmet = books whose
// file is below the profile's cutoff format (upgraded by the backlog pass).
// Searching happens only when a book is added, on release day, in the weekly
// backlog pass — or right here.
export function WantedView({ libraries }: { libraries: ApiLibrary[] }) {
  const { data, loading, reload } = useApi(() => acquisition.wanted(), 5_000)
  const missing = useMemo(() => data?.books ?? [], [data])
  const cutoffUnmet = useMemo(() => data?.cutoffUnmet ?? [], [data])
  const [tab, setTab] = useState<Tab>("missing")
  const [searchBook, setSearchBook] = useState<ApiBook | null>(null)
  const libName = (id?: number) => libraries.find(l => l.id === id)?.name ?? ""

  const books = tab === "missing" ? missing : cutoffUnmet

  return (
    <section>
      <Folio title="Wanted"
        meta={loading ? "loading…" : `${missing.length} missing · ${cutoffUnmet.length} below cutoff`}
        end={
          <Chips options={[{ v: "missing" as Tab, label: `Missing (${missing.length})` }, { v: "cutoff" as Tab, label: `Cutoff unmet (${cutoffUnmet.length})` }]}
            value={tab} onChange={setTab} />
        }
      />
      {!loading && books.length === 0 && (
        <p className="max-w-[52ch] text-[13.5px] leading-relaxed text-muted-foreground">
          {tab === "missing"
            ? "Nothing is missing — every monitored book is on the shelf."
            : "No upgrades pending — every file meets its profile's cutoff."}
        </p>
      )}
      <div className="border-t">
        {books.map(b => {
          const [c1, c2] = hashColors(b.title)
          return (
            <div key={`${b.id}-${b.libraryId}`} className="grid grid-cols-[40px_1fr_auto] items-center gap-4 border-b border-linesoft px-1 py-3">
              <MiniCover c1={c1} c2={c2} src={coverUrl(b.id)} ribbon={tab === "missing" ? "want" : "info"} />
              <div>
                <div className="text-[13.5px] font-semibold">
                  {b.title}
                  {b.seriesName && <Tag kind="dim" className="ml-1.5">{b.seriesName}{b.seriesNum ? ` #${b.seriesNum}` : ""}</Tag>}
                  {tab === "cutoff" && b.fileFormat && <Tag kind="want" className="ml-1.5">{b.fileFormat} → upgrade</Tag>}
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                  {b.author}
                  {b.libraryId ? <Tag kind="dim">{libName(b.libraryId)}</Tag> : null}
                  {tab === "missing" && b.releaseDate ? <Tag kind="dim">{fmtRelease(b.releaseDate)}</Tag> : null}
                </div>
              </div>
              <Button variant="outline" size="icon" className="h-[29px] w-[29px]" aria-label={`Search for ${b.title}`}
                onClick={() => setSearchBook(b)}>
                <Search className="h-3.5 w-3.5" />
              </Button>
            </div>
          )
        })}
      </div>

      <ReleasesDialog
        book={searchBook}
        open={searchBook !== null}
        onOpenChange={v => { if (!v) setSearchBook(null) }}
        onGrabbed={() => { toast("Watch Activity for progress"); reload() }}
      />
    </section>
  )
}
