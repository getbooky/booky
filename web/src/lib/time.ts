/**
 * Local-time rendering for the timestamps the API sends.
 *
 * Every stamp on the wire is RFC3339 UTC (see catalog.SQLTime on the server),
 * which is the only reason these work: an unzoned "2026-08-12 21:14:03" is
 * read by the browser as LOCAL time, quietly shifting the whole feed by the
 * viewer's offset. With the zone attached, the browser converts, and Booky
 * shows times in whatever timezone the person looking at it is in.
 */

function parse(iso?: string): Date | null {
  if (!iso) return null
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? null : d
}

/** Just the clock: "21:14" (or "9:14 PM" — the locale decides). */
export function formatClock(iso?: string): string {
  const d = parse(iso)
  return d ? d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }) : ""
}

/**
 * The compact form for a feed line: the time alone for today, the date and
 * time earlier this year, and the bare date for anything older — enough to
 * place an event without a full timestamp on every row.
 */
export function formatWhen(iso?: string): string {
  const d = parse(iso)
  if (!d) return ""
  const now = new Date()
  if (d.toDateString() === now.toDateString()) return formatClock(iso)
  if (d.getFullYear() !== now.getFullYear()) {
    return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })
  }
  return d.toLocaleString(undefined, { day: "numeric", month: "short", hour: "numeric", minute: "2-digit" })
}

/** The whole local timestamp, for the tooltip behind a compact one. */
export function formatFull(iso?: string): string {
  const d = parse(iso)
  return d ? d.toLocaleString() : ""
}

/** "just now", "6m", "3h", "2d" — how long a queue row has been sitting. */
export function formatAge(iso?: string): string {
  const d = parse(iso)
  if (!d) return ""
  const secs = Math.max(0, (Date.now() - d.getTime()) / 1000)
  if (secs < 60) return "just now"
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}m`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}
