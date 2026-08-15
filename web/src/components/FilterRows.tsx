import { useMemo, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import type { ApiBook } from "@/api"
import { X } from "lucide-react"
import { cn } from "@/lib/utils"

export interface ScopeFilter {
  field: string
  op: "is" | "not"
  value: string
}

export const FILTER_FIELDS = ["Author", "Series", "Genre", "Format", "Status", "Language"] as const

// valuesFor feeds the suggestion list under each value box — typing stays free-form.
export function valuesFor(books: ApiBook[], field: string): string[] {
  const uniq = (vals: (string | undefined)[]) => [...new Set(vals.filter((v): v is string => !!v))].sort()
  switch (field) {
    case "Author": return uniq(books.map(b => b.author))
    case "Series": return uniq(books.map(b => b.seriesName))
    case "Genre": return uniq(books.flatMap(b => b.genres ?? []))
    case "Format": return uniq(books.map(b => b.fileFormat?.toUpperCase()))
    case "Status": return ["On shelf", "Missing", "Unmonitored"]
    case "Language": return uniq(books.map(b => b.language))
    default: return []
  }
}

// fuzzyScore ranks target against query: null when the query's characters
// don't all appear in order, otherwise higher is better — a whole substring
// (earlier is better) beats scattered letters, and consecutive hits beat
// lone ones. An empty query matches everything at rank 0.
export function fuzzyScore(query: string, target: string): number | null {
  const q = query.trim().toLowerCase()
  if (!q) return 0
  const t = target.toLowerCase()
  const at = t.indexOf(q)
  if (at >= 0) return 1000 - at
  let ti = 0
  let score = 0
  for (const ch of q) {
    const found = t.indexOf(ch, ti)
    if (found < 0) return null
    score += found === ti ? 5 : 1
    ti = found + 1
  }
  return score
}

export const fuzzyMatch = (query: string, target: string) => fuzzyScore(query, target) !== null

// The value box: a free-form input with its own suggestion dropdown. Native
// datalists don't survive Radix popovers/dialogs, so this rolls the popup by
// hand — opens on focus, fuzzy-ranks while typing, arrows/enter to pick.
function SuggestInput({ value, options, placeholder, onChange }: {
  value: string
  options: string[]
  placeholder: string
  onChange: (v: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [hi, setHi] = useState(0)
  const ranked = useMemo(() => {
    const scored: { o: string; s: number }[] = []
    for (const o of options) {
      const s = fuzzyScore(value, o)
      if (s !== null) scored.push({ o, s })
    }
    scored.sort((a, b) => b.s - a.s || a.o.localeCompare(b.o))
    return scored.map(x => x.o).slice(0, 12)
  }, [options, value])
  const cursor = Math.min(hi, ranked.length - 1)
  const shown = open && ranked.length > 0
  const pick = (v: string) => { onChange(v); setOpen(false) }
  return (
    <div className="relative flex-1">
      <Input
        className="h-9 w-full"
        value={value}
        placeholder={placeholder}
        onChange={e => { onChange(e.target.value); setOpen(true); setHi(0) }}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={e => {
          if (!shown) return
          if (e.key === "ArrowDown") { e.preventDefault(); setHi(Math.min(cursor + 1, ranked.length - 1)) }
          else if (e.key === "ArrowUp") { e.preventDefault(); setHi(Math.max(cursor - 1, 0)) }
          else if (e.key === "Enter") { e.preventDefault(); pick(ranked[cursor]) }
          else if (e.key === "Escape") { e.stopPropagation(); setOpen(false) }
        }}
      />
      {shown && (
        <div className="absolute left-0 right-0 top-full z-50 mt-1 max-h-[210px] overflow-y-auto rounded-xl border bg-popover p-1 shadow-md">
          {ranked.map((o, i) => (
            <button key={o} type="button"
              className={cn(
                "block w-full truncate rounded-lg px-2.5 py-1.5 text-left text-[13px]",
                i === cursor ? "bg-accent text-accent-foreground" : "hover:bg-accent/60"
              )}
              onMouseDown={e => e.preventDefault()}
              onMouseEnter={() => setHi(i)}
              onClick={() => pick(o)}>
              {o}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

// FilterRows is the builder shared by the library filter popover and the
// new-scope dialog: each row is field · is / is not · value.
export function FilterRows({ rows, onChange, books }: {
  rows: ScopeFilter[]
  onChange: (rows: ScopeFilter[]) => void
  books: ApiBook[]
}) {
  const patch = (i: number, p: Partial<ScopeFilter>) =>
    onChange(rows.map((r, j) => (j === i ? { ...r, ...p } : r)))

  return (
    <div className="flex flex-col gap-2">
      {rows.map((row, i) => (
        <div key={i} className="flex items-center gap-2">
          <Select value={row.field} onValueChange={f => patch(i, { field: f, value: "" })}>
            <SelectTrigger className="h-9 w-[104px] shrink-0"><SelectValue /></SelectTrigger>
            <SelectContent className="rounded-xl">
              {FILTER_FIELDS.map(f => <SelectItem key={f} value={f}>{f}</SelectItem>)}
            </SelectContent>
          </Select>
          <Select value={row.op} onValueChange={op => patch(i, { op: op as ScopeFilter["op"] })}>
            <SelectTrigger className="h-9 w-[84px] shrink-0"><SelectValue /></SelectTrigger>
            <SelectContent className="rounded-xl">
              <SelectItem value="is">is</SelectItem>
              <SelectItem value="not">is not</SelectItem>
            </SelectContent>
          </Select>
          <SuggestInput value={row.value} placeholder={`${row.field}…`}
            options={valuesFor(books, row.field)}
            onChange={v => patch(i, { value: v })} />
          <Button variant="outline" size="icon" className="h-9 w-9 shrink-0" aria-label="Remove filter"
            onClick={() => onChange(rows.filter((_, j) => j !== i))}>
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      ))}
      <Button variant="outline" className="h-8 w-fit border-dashed"
        onClick={() => onChange([...rows, { field: "Author", op: "is", value: "" }])}>
        + Add filter
      </Button>
    </div>
  )
}
