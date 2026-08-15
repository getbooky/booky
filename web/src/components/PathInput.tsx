import { useEffect, useRef, useState } from "react"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

// PathInput: a text input for filesystem paths that suggests server-side
// directories as you type — pick one and keep drilling down.
export function PathInput({ value, onChange, placeholder, className, autoFocus }: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
  className?: string
  autoFocus?: boolean
}) {
  const [dirs, setDirs] = useState<string[]>([])
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const wrapRef = useRef<HTMLDivElement>(null)
  const debounce = useRef<ReturnType<typeof setTimeout>>(undefined)

  useEffect(() => {
    clearTimeout(debounce.current)
    if (!open) return
    debounce.current = setTimeout(async () => {
      try {
        const res = await fetch(`/api/v1/system/browse?path=${encodeURIComponent(value || "/data/")}`)
        if (!res.ok) return
        const body = await res.json() as { dirs: string[] }
        setDirs(body.dirs ?? [])
        setActive(-1)
      } catch { /* offline or unauthorized — just no suggestions */ }
    }, 150)
    return () => clearTimeout(debounce.current)
  }, [value, open])

  // close on outside click
  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener("mousedown", onDown)
    return () => document.removeEventListener("mousedown", onDown)
  }, [])

  const pick = (dir: string) => {
    onChange(dir)
    setActive(-1)
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (!open || dirs.length === 0) return
    if (e.key === "ArrowDown") {
      e.preventDefault()
      setActive(a => (a + 1) % dirs.length)
    } else if (e.key === "ArrowUp") {
      e.preventDefault()
      setActive(a => (a - 1 + dirs.length) % dirs.length)
    } else if (e.key === "Tab" || (e.key === "Enter" && active >= 0)) {
      e.preventDefault()
      pick(dirs[active >= 0 ? active : 0])
    } else if (e.key === "Escape") {
      setOpen(false)
    }
  }

  return (
    <div ref={wrapRef} className="relative">
      <Input value={value} placeholder={placeholder} autoFocus={autoFocus}
        className={cn("font-label text-[11.5px]", className)}
        onChange={e => { onChange(e.target.value); setOpen(true) }}
        onFocus={() => setOpen(true)}
        onKeyDown={onKeyDown} />
      {open && dirs.length > 0 && (
        <div className="absolute left-0 right-0 top-full z-50 mt-1 max-h-[220px] overflow-y-auto rounded-md border bg-surface shadow-md">
          {dirs.map((d, i) => (
            <button key={d} type="button"
              className={cn(
                "block w-full truncate px-3 py-1.5 text-left font-label text-[11.5px] text-muted-foreground hover:bg-surface2 hover:text-foreground",
                i === active && "bg-surface2 text-foreground"
              )}
              onMouseDown={e => { e.preventDefault(); pick(d) }}>
              {d}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
