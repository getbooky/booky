import type { ReactNode } from "react"
import { cn } from "@/lib/utils"
import { formatColor } from "@/components/Cover"

export function Tag({ kind, children, className }: { kind: "good" | "info" | "want" | "dim" | "brand"; children: ReactNode; className?: string }) {
  const styles = {
    good: "bg-good/15 text-good",
    info: "bg-info/15 text-info",
    want: "bg-want/15 text-want",
    dim: "bg-surface3 text-muted-foreground",
    brand: "bg-brass/15 text-brass",
  }
  return (
    <span className={cn("inline-block rounded-md px-2 py-0.5 text-[11px] font-semibold", styles[kind], className)}>
      {children}
    </span>
  )
}

// Format chip tinted to match the cover badge's per-format color, so EPUB
// reads green everywhere, AZW3 amber, and so on.
export function Fmt({ children }: { children: ReactNode }) {
  const c = typeof children === "string" ? formatColor(children) : undefined
  return (
    <span className="font-label rounded-sm border px-1.5 py-px text-[10px] font-bold uppercase tracking-[0.08em]"
      style={c ? { borderColor: `${c}88`, background: `${c}22`, color: c } : undefined}>
      {children}
    </span>
  )
}

export function Folio({ title, meta, end }: { title: string; meta?: string; end?: ReactNode }) {
  return (
    <>
      <div className="flex flex-wrap items-baseline gap-x-5 gap-y-1">
        <h1 className="font-book text-[34px] font-bold tracking-tight text-balance">{title}</h1>
        {meta && <span className="mono-label text-muted-foreground">{meta}</span>}
        {end && <div className="ml-auto flex items-center gap-2.5">{end}</div>}
      </div>
      <hr className="mb-6 mt-3.5 border-0 border-t-[3px] border-double border-border" />
    </>
  )
}

export function Chips<T extends string>({ options, value, onChange }: { options: { v: T; label: string }[]; value: T; onChange: (v: T) => void }) {
  return (
    <div className="flex w-fit flex-wrap overflow-hidden rounded-lg border">
      {options.map((o, i) => (
        <button
          key={o.v}
          onClick={() => onChange(o.v)}
          className={cn(
            "px-3.5 py-1.5 text-[12.5px] font-medium transition-colors",
            i > 0 && "border-l",
            o.v === value
              ? "bg-brass font-semibold text-brass-ink"
              : "text-muted-foreground hover:bg-surface2 hover:text-foreground"
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

export function SectionLabel({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("mono-label mb-2 mt-8 text-faint first:mt-0", className)}>{children}</div>
}
