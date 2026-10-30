import { Check, Tag as TagIcon, User } from "lucide-react"
import { tags as tagsApi } from "@/api"
import type { ApiTag } from "@/api"
import { useApi } from "@/hooks/use-api"
import { cn } from "@/lib/utils"

// Tags are who-sees-what, so they get one look everywhere: rounded pills,
// brass when on, a person glyph on personal tags. The same component picks
// a book's audience, a user's tags, a device's tags and a list's audience.

/** All tags the caller may see (every tag for an admin, their own for
 * anyone else), loaded once per mount. */
export function useTags() {
  const q = useApi(() => tagsApi.list())
  const all = q.data?.tags ?? []
  return { tags: all, byId: new Map(all.map(t => [t.id, t])), reload: q.reload, loading: q.loading }
}

/** The label a tag reads as: shared tags by name, personal ones as the
 * person's name with "(personal)" when there's room for it. */
export function tagLabel(t: ApiTag, short = false): string {
  return t.personal && !short ? `${t.name} (personal)` : t.name
}

export function TagPill({ tag, on, onClick, disabled, size = "md" }: {
  tag: ApiTag
  on: boolean
  onClick?: () => void
  disabled?: boolean
  size?: "sm" | "md"
}) {
  const Icon = tag.personal ? User : TagIcon
  const cls = cn(
    "inline-flex items-center gap-1.5 rounded-full border font-medium transition-colors",
    size === "sm" ? "px-2 py-0.5 text-[11px]" : "px-2.5 py-1 text-[12px]",
    on
      ? "border-brass/50 bg-brass/15 text-brass"
      : "border-linesoft bg-surface2/60 text-muted-foreground",
    onClick && !disabled && !on && "hover:border-brass/40 hover:text-foreground",
    disabled && "opacity-50",
  )
  const body = (
    <>
      {on && onClick ? <Check className="h-3 w-3" /> : <Icon className="h-3 w-3" />}
      {tagLabel(tag, size === "sm")}
    </>
  )
  if (!onClick) return <span className={cls}>{body}</span>
  return (
    <button type="button" className={cls} disabled={disabled} aria-pressed={on} onClick={onClick}>
      {body}
    </button>
  )
}

/** A toggleable set of tag pills. `allowed` limits which tags are offered
 * (a device can only use its owner's tags); `hidePersonal` drops personal
 * tags other than `keepPersonal`. */
export function TagPicker({ tags, value, onChange, allowed, hidePersonal, keepPersonal, empty }: {
  tags: ApiTag[]
  value: number[]
  onChange: (next: number[]) => void
  allowed?: number[]
  hidePersonal?: boolean
  keepPersonal?: number
  empty?: string
}) {
  const shown = tags.filter(t =>
    (!allowed || allowed.includes(t.id)) &&
    (!hidePersonal || !t.personal || t.id === keepPersonal))
  if (shown.length === 0) {
    return <p className="text-[12.5px] text-muted-foreground">{empty ?? "No tags yet — create one in Settings → Tags."}</p>
  }
  const toggle = (id: number) =>
    onChange(value.includes(id) ? value.filter(x => x !== id) : [...value, id])
  return (
    <div className="flex flex-wrap gap-1.5">
      {shown.map(t => <TagPill key={t.id} tag={t} on={value.includes(t.id)} onClick={() => toggle(t.id)} />)}
    </div>
  )
}
