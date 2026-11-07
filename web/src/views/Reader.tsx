import { useEffect, useRef, useState } from "react"
import { ArrowLeft, List, Pause, Play, SkipBack, SkipForward } from "lucide-react"
import { api, audiobooks, bookFileUrl } from "@/api"
import type { ApiBook } from "@/api"
import { cn } from "@/lib/utils"
import type { RelocateDetail, TOCItem, View } from "@/vendor/foliate-js/view.js"
import { formatDuration } from "@/lib/time"
import { ACTIVE_CLASS, ReadAlongPlayer } from "./readAlong"
import type { PlayerState } from "./readAlong"

// The reader keeps its own theme, independent of the app's: you can read on
// a dark page while the app is in daylight mode and vice versa. Both palettes
// are Booky's own (index.css), delivered as local --r-* vars so the app theme
// never bleeds into the reading surface.
const THEMES = {
  dark: {
    bg: "hsl(30 25% 6%)", fg: "hsl(40 39% 88%)",
    surface: "hsl(30 22% 9% / 0.92)", surface2: "hsl(30 24% 12%)", surface3: "hsl(32 25% 15%)",
    line: "hsl(33 27% 17%)", linesoft: "hsl(33 27% 13%)",
    faint: "hsl(33 17% 36%)", muted: "hsl(34 17% 57%)",
    brass: "hsl(38 70% 51%)", brassInk: "hsl(33 70% 6%)",
    scrim: "hsl(30 25% 3% / 0.55)", shadow: "0 8px 28px rgba(0,0,0,0.25)",
  },
  light: {
    bg: "hsl(42 36% 91%)", fg: "hsl(35 25% 14%)",
    surface: "hsl(44 48% 95% / 0.94)", surface2: "hsl(43 40% 90%)", surface3: "hsl(41 36% 84%)",
    line: "hsl(40 29% 76%)", linesoft: "hsl(41 31% 82%)",
    faint: "hsl(38 17% 56%)", muted: "hsl(38 19% 39%)",
    brass: "hsl(38 75% 35%)", brassInk: "hsl(44 73% 97%)",
    scrim: "hsl(40 30% 30% / 0.35)", shadow: "0 8px 28px rgba(60,48,28,0.18)",
  },
} as const
type ReaderTheme = keyof typeof THEMES

// Injected into the book's documents. Color overrides are forced: books that
// hardcode black-on-white would otherwise be unreadable on the dark theme.
const contentCSS = (theme: ReaderTheme, fontSize: number) => {
  const t = THEMES[theme]
  return `
    @namespace epub "http://www.idpf.org/2007/ops";
    html { font-size: ${fontSize}px; font-family: "Iowan Old Style", "Palatino Linotype", Palatino, Georgia, serif; }
    html, body, p, li, blockquote, dd, dt, div, span, h1, h2, h3, h4, h5, h6, td, th {
      color: ${t.fg} !important;
      background-color: transparent !important;
    }
    a:link, a:visited { color: ${t.brass} !important; }
    p, li, blockquote, dd { line-height: 1.62; -webkit-hyphens: auto; hyphens: auto; widows: 2; }
    pre { white-space: pre-wrap !important; }
    img, svg { max-width: 100%; }
    .${ACTIVE_CLASS} {
      background-color: ${theme === "dark" ? "hsl(38 70% 51% / 0.28)" : "hsl(38 80% 60% / 0.35)"} !important;
      border-radius: 3px; box-decoration-break: clone; -webkit-box-decoration-break: clone;
    }
    aside[epub|type~="endnote"], aside[epub|type~="footnote"],
    aside[epub|type~="note"], aside[epub|type~="rearnote"] { display: none; }
  `
}

type Loc = {
  fraction: number
  cfi: string
  chapter: string
  chapterHref?: string
  locCurrent?: number
  locTotal?: number
}

function flattenToc(items: TOCItem[], depth = 0): { item: TOCItem; depth: number }[] {
  return items.flatMap(item => [
    { item, depth },
    ...(item.subitems ? flattenToc(item.subitems, depth + 1) : []),
  ])
}

const RATES = [0.8, 1, 1.15, 1.3, 1.5, 1.75, 2]

// readAlong opens the read-along edition with the audiobook playing over it;
// the place you're at is the listen position, not the reading progress (the
// edition's markup differs from the ebook's, so its locations don't carry).
export function ReaderView({ book, readAlong = false, onClose }: { book: ApiBook; readAlong?: boolean; onClose: () => void }) {
  const containerRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<View | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [chrome, setChrome] = useState(true)
  const [tocOpen, setTocOpen] = useState(false)
  const [aaOpen, setAaOpen] = useState(false)
  const [toc, setToc] = useState<TOCItem[]>([])
  const [loc, setLoc] = useState<Loc | null>(null)
  const [theme, setTheme] = useState<ReaderTheme>(() =>
    localStorage.getItem("booky-reader-theme") === "light" ? "light" : "dark")
  const playerRef = useRef<ReadAlongPlayer | null>(null)
  const [player, setPlayer] = useState<PlayerState | null>(null)
  const [fontSize, setFontSize] = useState(() => {
    const v = parseInt(localStorage.getItem("booky-reader-font") ?? "", 10)
    return v >= 14 && v <= 24 ? v : 18
  })

  // popover/chrome state lives in refs too, so stable listeners see it
  const ui = useRef({ tocOpen, aaOpen })
  ui.current = { tocOpen, aaOpen }

  // ---- progress saving: debounced on relocate, flushed on exit/hide ----
  const pending = useRef<{ cfi: string; fraction: number } | null>(null)
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const flushSave = () => {
    if (saveTimer.current) { clearTimeout(saveTimer.current); saveTimer.current = null }
    const p = pending.current
    pending.current = null
    if (p?.cfi) void api.saveReadingProgress(book.id, p.cfi, p.fraction).catch(() => {})
  }
  const scheduleSave = (cfi: string, fraction: number) => {
    pending.current = { cfi, fraction }
    if (saveTimer.current) clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(flushSave, 1500)
  }

  // ---- tap zones: left 28% back, right 28% forward, middle toggles chrome ----
  const handleTap = (x: number) => {
    const w = window.innerWidth
    if (ui.current.tocOpen || ui.current.aaOpen) { setTocOpen(false); setAaOpen(false); return }
    if (x < w * 0.28) void viewRef.current?.goLeft()
    else if (x > w * 0.72) void viewRef.current?.goRight()
    else setChrome(c => !c)
  }
  const handleKey = (e: KeyboardEvent) => {
    if (e.key === "ArrowLeft") void viewRef.current?.goLeft()
    else if (e.key === "ArrowRight") void viewRef.current?.goRight()
    else if (e.key === " " && playerRef.current) { e.preventDefault(); togglePlay() }
    else if (e.key === " ") { e.preventDefault(); void viewRef.current?.goRight() }
    else if (e.key === "Escape") {
      if (ui.current.tocOpen || ui.current.aaOpen) { setTocOpen(false); setAaOpen(false) }
      else exit()
    }
  }
  const exit = () => { flushSave(); playerRef.current?.save(true); onClose() }
  // play picks up at the page you're looking at if you've turned away from
  // the narration; otherwise it carries on
  const togglePlay = () => {
    const p = playerRef.current
    if (!p) return
    if (p.playing) p.pause()
    else p.playFromPage()
  }

  useEffect(() => {
    let cancelled = false
    // the app behind the overlay must not scroll while reading
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = "hidden"

    const onRelocate = (e: Event) => {
      const d = (e as CustomEvent<RelocateDetail>).detail
      const fraction = d.fraction ?? 0
      setLoc({
        fraction,
        cfi: d.cfi ?? "",
        chapter: d.tocItem?.label?.trim() ?? "",
        chapterHref: d.tocItem?.href,
        locCurrent: d.location?.current,
        locTotal: d.location?.total,
      })
      if (d.cfi && !readAlong) scheduleSave(d.cfi, fraction)
    }
    // book documents live in iframes, so taps and keys inside them need
    // their own listeners; tap x is mapped back into window coordinates
    const onDocLoad = (e: Event) => {
      const { doc } = (e as CustomEvent<{ doc: Document }>).detail
      doc.addEventListener("click", ev => {
        if (ev.defaultPrevented) return // a link the engine already handled
        if (ev.view?.getSelection()?.toString()) return // selecting text
        const frame = ev.view?.frameElement
        const x = frame ? frame.getBoundingClientRect().left + ev.clientX : ev.clientX
        handleTap(x)
      })
      doc.addEventListener("keydown", handleKey)
    }
    const onHide = () => {
      if (document.visibilityState !== "hidden") return
      flushSave()
      playerRef.current?.save(true)
    }

    ;(async () => {
      try {
        await import("@/vendor/foliate-js/view.js")
        const [res, progress, timing, listen] = await Promise.all([
          fetch(readAlong ? audiobooks.readAlongUrl(book.id, "readalong.epub") : bookFileUrl(book.id)),
          readAlong
            ? { locator: "", percent: 0 }
            : api.readingProgress(book.id).catch(() => ({ locator: "", percent: 0 })),
          readAlong ? audiobooks.timing(book.id) : null,
          readAlong ? audiobooks.position(book.id).catch(() => ({ position: 0 })) : null,
        ])
        if (!res.ok) {
          throw new Error(res.status === 404
            ? (readAlong ? "The read-along isn't ready for this book." : "No file on the shelf for this book.")
            : `Couldn't load the book file (${res.status}).`)
        }
        const blob = await res.blob()
        if (cancelled) return
        const name = readAlong ? "readalong.epub" : book.filePath?.split("/").pop() || `${book.title}.epub`
        const file = new File([blob], name, { type: blob.type })

        const view = document.createElement("foliate-view") as unknown as View
        view.style.cssText = "display:block;width:100%;height:100%"
        view.addEventListener("relocate", onRelocate)
        view.addEventListener("load", onDocLoad)
        containerRef.current?.append(view)
        viewRef.current = view

        await view.open(file)
        if (cancelled) return
        setToc(view.book.toc ?? [])
        view.renderer.setAttribute("max-inline-size", "720px")
        view.renderer.setAttribute("max-column-count", "1")
        view.renderer.setAttribute("gap", "6%")
        view.renderer.setStyles?.(contentCSS(
          (localStorage.getItem("booky-reader-theme") === "light" ? "light" : "dark"),
          parseInt(localStorage.getItem("booky-reader-font") ?? "18", 10) || 18,
        ))
        await view.init({ lastLocation: progress.locator || null, showTextStart: !progress.locator })
        if (cancelled) return
        if (timing) {
          const p = new ReadAlongPlayer(book.id, timing, view, setPlayer)
          playerRef.current = p
          p.setMetadata(book.title, book.author ?? "")
          // back to where you left off listening (the page follows)
          p.seek(listen?.position ?? 0)
        }
        setLoading(false)
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      }
    })()

    window.addEventListener("keydown", handleKey)
    document.addEventListener("visibilitychange", onHide)
    return () => {
      cancelled = true
      window.removeEventListener("keydown", handleKey)
      document.removeEventListener("visibilitychange", onHide)
      document.body.style.overflow = prevOverflow
      flushSave()
      playerRef.current?.destroy()
      playerRef.current = null
      viewRef.current?.close()
      viewRef.current?.remove()
      viewRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [book.id])

  // theme / text size apply live to the open book
  useEffect(() => {
    localStorage.setItem("booky-reader-theme", theme)
    localStorage.setItem("booky-reader-font", String(fontSize))
    viewRef.current?.renderer.setStyles?.(contentCSS(theme, fontSize))
  }, [theme, fontSize])

  const t = THEMES[theme]
  const vars = {
    "--r-bg": t.bg, "--r-fg": t.fg, "--r-surface": t.surface, "--r-surface2": t.surface2,
    "--r-surface3": t.surface3, "--r-line": t.line, "--r-linesoft": t.linesoft,
    "--r-faint": t.faint, "--r-muted": t.muted, "--r-brass": t.brass,
    "--r-brass-ink": t.brassInk, "--r-scrim": t.scrim,
  } as React.CSSProperties
  const pct = player && player.duration > 0
    ? Math.round(100 * player.position / player.duration)
    : loc ? Math.round(loc.fraction * 100) : 0
  const flatToc = flattenToc(toc)
  const barBase = "fixed left-3 right-3 z-20 flex items-center gap-2 rounded-2xl border px-3 " +
    "border-(--r-linesoft) bg-(--r-surface) shadow-(--r-shadow) backdrop-blur-md " +
    "transition-[opacity,transform] duration-200"
  const iconBtn = "flex h-9 w-9 shrink-0 items-center justify-center rounded-[10px] " +
    "text-(--r-muted) hover:bg-(--r-surface3) hover:text-(--r-fg) " +
    "focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-(--r-brass)"

  return (
    <div className="fixed inset-0 z-50 bg-(--r-bg) text-(--r-fg) transition-colors"
      style={{ ...vars, boxShadow: "var(--r-shadow)" } as React.CSSProperties}>

      {/* reading surface — inset so text clears the floating bars */}
      <div ref={containerRef}
        className={cn("absolute inset-x-0 top-[calc(env(safe-area-inset-top)+4.2rem)]",
          readAlong ? "bottom-[5.4rem]" : "bottom-[3.6rem]")}
        onClick={e => handleTap(e.clientX)} />

      {loading && (
        <div className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-3 bg-(--r-bg)">
          <div className="font-book text-lg">{book.title}</div>
          <div className="mono-label text-[10px] text-(--r-faint)">Opening…</div>
        </div>
      )}
      {error && (
        <div className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-4 bg-(--r-bg) px-8 text-center">
          <div className="font-book text-lg">{book.title}</div>
          <div className="text-sm text-(--r-muted)">{error}</div>
          <button onClick={exit}
            className="rounded-[10px] bg-(--r-brass) px-4 py-2 text-sm font-semibold text-(--r-brass-ink)">
            Back
          </button>
        </div>
      )}

      {/* top bar */}
      <div className={cn(barBase, "top-[calc(env(safe-area-inset-top)+0.75rem)] py-2",
        !chrome && "pointer-events-none -translate-y-2 opacity-0")}>
        <button className={iconBtn} aria-label="Back" title="Back" onClick={exit}>
          <ArrowLeft className="h-[18px] w-[18px]" />
        </button>
        <div className="min-w-0 flex-1 leading-tight">
          <div className="truncate text-[14.5px] font-semibold">{book.title}</div>
          <div className="mono-label truncate text-[10px] text-(--r-muted)">
            {book.author}{readAlong ? " · read-along" : book.fileFormat ? ` · ${book.fileFormat.toUpperCase()}` : ""}
          </div>
        </div>
        <button className={iconBtn} aria-label="Table of contents" title="Contents"
          onClick={() => { setAaOpen(false); setTocOpen(o => !o) }}>
          <List className="h-[18px] w-[18px]" />
        </button>
        <button className={cn(iconBtn, "font-book text-[15px] font-semibold")} aria-label="Display options"
          title="Display" onClick={() => { setTocOpen(false); setAaOpen(o => !o) }}>
          Aa
        </button>
      </div>

      {/* bottom bar — in read-along it's the player, and stays put */}
      {readAlong ? (
        <div className={cn(barBase, "bottom-3 flex-col items-stretch gap-1 py-2")}>
          <input type="range" aria-label="Position" min={0} max={Math.max(1, Math.round(player?.duration ?? 0))} step={1}
            value={Math.round(player?.position ?? 0)} disabled={!player}
            onChange={e => playerRef.current?.seek(Number(e.target.value))}
            className="h-1 w-full cursor-pointer accent-(--r-brass)" />
          <div className="flex items-center gap-1">
            <span className="mono-label w-[4.5rem] shrink-0 text-[10px] text-(--r-muted)">
              {formatDuration(player?.position ?? 0)}
            </span>
            <div className="flex flex-1 items-center justify-center gap-1">
              <button className={iconBtn} aria-label="Previous sentence" title="Previous sentence"
                disabled={!player} onClick={() => playerRef.current?.skip(-1)}>
                <SkipBack className="h-[18px] w-[18px]" />
              </button>
              <button aria-label={player?.playing ? "Pause" : "Play"} disabled={!player} onClick={togglePlay}
                className="flex h-10 w-10 items-center justify-center rounded-full bg-(--r-brass) text-(--r-brass-ink) disabled:opacity-50">
                {player?.playing ? <Pause className="h-[18px] w-[18px]" /> : <Play className="ml-0.5 h-[18px] w-[18px]" />}
              </button>
              <button className={iconBtn} aria-label="Next sentence" title="Next sentence"
                disabled={!player} onClick={() => playerRef.current?.skip(1)}>
                <SkipForward className="h-[18px] w-[18px]" />
              </button>
            </div>
            <button className={cn(iconBtn, "mono-label w-[4.5rem] justify-end text-[11px]")} disabled={!player}
              aria-label="Playback speed" title="Playback speed"
              onClick={() => {
                const r = player?.rate ?? 1
                playerRef.current?.setRate(RATES[(RATES.findIndex(x => x >= r - 0.01) + 1) % RATES.length])
              }}>
              {Number((player?.rate ?? 1).toFixed(2))}×
            </button>
          </div>
          {player?.error && <div className="text-center text-[12px] text-(--r-muted)">{player.error}</div>}
        </div>
      ) : (
      <div className={cn(barBase, "bottom-3 justify-between py-1.5",
        !chrome && "pointer-events-none translate-y-2 opacity-0")}>
        <span className="font-book min-w-0 truncate text-[12.5px] italic">{loc?.chapter || " "}</span>
        <span className="mono-label shrink-0 text-[10px] text-(--r-muted)">
          {loc?.locTotal ? `loc ${loc.locCurrent} / ${loc.locTotal} · ` : ""}{pct}%
        </span>
      </div>
      )}
      {/* progress hairline survives chrome-hidden mode */}
      <div className="fixed inset-x-0 bottom-0 z-20 h-[2px]">
        <div className="h-full bg-(--r-brass) transition-[width]" style={{ width: `${pct}%` }} />
      </div>

      {/* TOC drawer */}
      <div className={cn("fixed inset-0 z-30 bg-(--r-scrim) transition-opacity",
        tocOpen ? "opacity-100" : "pointer-events-none opacity-0")}
        onClick={() => setTocOpen(false)} />
      <nav aria-label="Table of contents"
        className={cn("fixed inset-y-0 left-0 z-40 flex w-[min(320px,85vw)] flex-col border-r",
          "border-(--r-line) bg-(--r-surface2) p-4 transition-transform duration-200",
          tocOpen ? "translate-x-0" : "-translate-x-full")}>
        <header className="flex items-baseline justify-between px-2 pb-3.5">
          <span className="text-[15px] font-semibold">Contents</span>
          <span className="mono-label text-[10px] text-(--r-faint)">
            {flatToc.length} {flatToc.length === 1 ? "entry" : "entries"}
          </span>
        </header>
        <div className="flex flex-col gap-0.5 overflow-y-auto">
          {flatToc.map(({ item, depth }, i) => {
            const current = !!item.href && item.href === loc?.chapterHref
            return (
              <button key={`${item.href ?? ""}-${i}`}
                className={cn("flex items-center gap-2.5 rounded-[10px] px-2.5 py-2 text-left text-[14px]",
                  "hover:bg-(--r-surface3)",
                  "focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-(--r-brass)",
                  current && "text-(--r-brass)")}
                style={{ paddingLeft: `${10 + depth * 14}px` }}
                onClick={() => {
                  setTocOpen(false)
                  if (item.href) void viewRef.current?.goTo(item.href)
                }}>
                <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full",
                  current ? "bg-(--r-brass)" : "bg-transparent")} />
                <span className="min-w-0 flex-1 truncate">{item.label.trim()}</span>
              </button>
            )
          })}
          {flatToc.length === 0 && (
            <div className="px-2.5 py-2 text-[13px] text-(--r-faint)">This book has no contents listing.</div>
          )}
        </div>
      </nav>

      {/* Aa popover */}
      <div role="dialog" aria-label="Display options"
        className={cn("fixed right-3.5 z-40 w-[250px] rounded-[14px] border p-3.5",
          "top-[calc(env(safe-area-inset-top)+4rem)]",
          "border-(--r-line) bg-(--r-surface2) shadow-(--r-shadow) transition-[opacity,transform] duration-150",
          aaOpen ? "translate-y-0 opacity-100" : "pointer-events-none -translate-y-1.5 opacity-0")}>
        <p className="mono-label mb-2 text-[10px] text-(--r-faint)">Theme</p>
        <div className="mb-3.5 flex gap-1.5">
          {(["dark", "light"] as const).map(v => (
            <button key={v} onClick={() => setTheme(v)}
              className={cn("flex-1 rounded-[10px] border py-2 text-[13px] capitalize",
                theme === v
                  ? "border-(--r-brass) bg-(--r-brass) font-semibold text-(--r-brass-ink)"
                  : "border-(--r-line) text-(--r-muted) hover:text-(--r-fg)")}>
              {v}
            </button>
          ))}
        </div>
        <p className="mono-label mb-2 text-[10px] text-(--r-faint)">Text size</p>
        <div className="flex items-center gap-2.5">
          <button aria-label="Smaller text" onClick={() => setFontSize(s => Math.max(14, s - 1))}
            className={cn(iconBtn, "font-book w-11 border border-(--r-line) text-[13px]")}>A−</button>
          <span className="mono-label flex-1 text-center text-[11px] text-(--r-muted)">{fontSize} px</span>
          <button aria-label="Larger text" onClick={() => setFontSize(s => Math.min(24, s + 1))}
            className={cn(iconBtn, "font-book w-11 border border-(--r-line) text-[17px]")}>A+</button>
        </div>
      </div>
    </div>
  )
}
