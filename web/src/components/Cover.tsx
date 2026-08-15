import { useEffect, useRef, useState, useSyncExternalStore } from "react"
import { coverGen, subscribeCovers } from "@/api"
import { cn } from "@/lib/utils"

const param = (url: string, key: string, value: string | number) =>
  `${url}${url.includes("?") ? "&" : "?"}${key}=${value}`

// A cover often lands seconds AFTER its book first renders (the watcher
// downloads it in the background), so a failed image load must not latch
// the gradient fallback for good. Retry a few times with growing delays and
// a cache-busting param; give up quietly if it still isn't there.
//
// The generation param is the other half: replacing a cover leaves its URL
// alone, and the browser holds the decoded image in memory for the life of
// the page, so every cover on screen needs a URL it hasn't seen before once
// any cover changes. Only sessions that actually change one pay for it, and
// the re-requests come back 304.
const COVER_RETRIES = 5
function useCoverSrc(src?: string): { imgSrc?: string; loaded: boolean; onError: () => void; onLoad: () => void } {
  const [attempt, setAttempt] = useState(0)
  // loaded gates the img's visibility: a missing cover must show the gradient
  // fallback, never the browser's broken-image glyph, while retries wait
  const [loaded, setLoaded] = useState(false)
  const gen = useSyncExternalStore(subscribeCovers, coverGen)
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => {
    setAttempt(0)
    setLoaded(false)
    return () => window.clearTimeout(timer.current)
  }, [src])
  const onError = () => {
    setLoaded(false)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setAttempt(a => a + 1), 5000 * (attempt + 1))
  }
  const onLoad = () => setLoaded(true)
  if (!src || attempt > COVER_RETRIES) return { imgSrc: undefined, loaded: false, onError, onLoad }
  let out = src
  if (gen) out = param(out, "v", gen)
  if (attempt > 0) out = param(out, "r", attempt)
  return { imgSrc: out, loaded, onError, onLoad }
}

export type RibbonKind = "good" | "info" | "want" | "dim"

// deterministic gradient for books whose cover isn't cached (yet)
const PALETTE: [string, string][] = [
  ["#1c3f5e", "#0d2033"], ["#8c2f2f", "#3d0f14"], ["#274a33", "#0f231a"],
  ["#4b3a78", "#1d1433"], ["#8a6a1f", "#3c2c08"], ["#245a63", "#0c262b"],
  ["#6e2c50", "#2b0f22"], ["#69201c", "#2d0a0d"], ["#33415e", "#131a29"],
  ["#7c5c22", "#33230a"], ["#3a5a2a", "#152309"], ["#513c73", "#20142f"],
]

export function hashColors(seed: string): [string, string] {
  let h = 0
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) | 0
  return PALETTE[Math.abs(h) % PALETTE.length]
}

// One color per file format so the badge reads at a glance: every EPUB is
// green, every AZW3 amber, and so on. Unknown formats fall back to slate.
const FORMAT_COLORS: Record<string, string> = {
  EPUB: "#2e8b57",
  AZW3: "#c8821e",
  AZW: "#c8821e",
  KFX: "#a8681a",
  MOBI: "#7050b0",
  PDF: "#c04040",
  CBZ: "#2f7bb8",
  CBR: "#2f7bb8",
  FB2: "#31908a",
  DJVU: "#96603c",
  TXT: "#5a6472",
  RTF: "#5a6472",
}
const FORMAT_FALLBACK = "#4a5260"

export function formatColor(fmt: string): string {
  return FORMAT_COLORS[fmt.trim().toUpperCase()] ?? FORMAT_FALLBACK
}

const RIB_BG: Record<RibbonKind, string> = {
  good: "bg-good",
  info: "bg-info",
  want: "bg-want",
  dim: "bg-faint",
}

export function Ribbon({ kind, mini }: { kind: RibbonKind; mini?: boolean }) {
  return (
    <span
      className={cn(
        "absolute z-10 [clip-path:polygon(0_0,100%_0,100%_100%,50%_82%,0_100%)] drop-shadow-sm",
        RIB_BG[kind],
        mini ? "-top-0.5 right-[5px] h-[30%] w-[9px]" : "-top-1 right-[9%] h-[27%] w-[15px]"
      )}
    />
  )
}

export function Cover({
  c1, c2, title, author, ribbon, progress, badge, className, src,
}: {
  c1: string
  c2: string
  title: string
  author: string
  ribbon?: RibbonKind
  progress?: number
  badge?: string
  className?: string
  // real cover image URL; gradient + typographic fallback when missing/failed
  src?: string
}) {
  // title/author text shows until a real image has painted
  const { imgSrc, loaded, onError, onLoad } = useCoverSrc(src)
  return (
    <div
      className={cn("cover-shine cover-shadow relative flex aspect-2/3 flex-col overflow-hidden rounded-[6px_10px_10px_6px] p-[11%] text-[#f2ecdd]", className)}
      style={{ background: `linear-gradient(155deg, ${c1}, ${c2})` }}
    >
      {imgSrc && (
        <img src={imgSrc} alt="" loading="lazy" onError={onError} onLoad={onLoad}
          className={cn("absolute inset-0 h-full w-full object-cover", !loaded && "invisible")} />
      )}
      {ribbon && <Ribbon kind={ribbon} />}
      {!loaded && (
        <div className="font-book text-[clamp(11px,1.05vw,15px)] font-bold leading-[1.14] text-balance">{title}</div>
      )}
      {!loaded && (
        <div className="mt-auto text-[clamp(8px,0.65vw,10px)] uppercase tracking-[0.14em] opacity-85">{author}</div>
      )}
      {progress !== undefined && (
        <div className="absolute inset-x-0 bottom-0 h-1 overflow-hidden rounded-b-[10px] bg-black/45">
          <i className="block h-full bg-info" style={{ width: `${progress}%` }} />
        </div>
      )}
      {badge && (
        <span className="font-label absolute bottom-1.5 right-1.5 rounded-sm px-1.5 py-0.5 text-[8.5px] font-bold tracking-[0.07em] text-white shadow-[0_1px_3px_rgba(0,0,0,0.5)]"
          style={{ background: formatColor(badge) }}>
          {badge}
        </span>
      )}
    </div>
  )
}

export function MiniCover({ c1, c2, ribbon, className, src }: { c1: string; c2: string; ribbon?: RibbonKind; className?: string; src?: string }) {
  const { imgSrc, loaded, onError, onLoad } = useCoverSrc(src)
  return (
    <div
      className={cn("cover-shadow relative aspect-2/3 w-10 shrink-0 overflow-hidden rounded-[4px_7px_7px_4px]", className)}
      style={{ background: `linear-gradient(155deg, ${c1}, ${c2})` }}
    >
      {imgSrc && (
        <img src={imgSrc} alt="" loading="lazy" onError={onError} onLoad={onLoad}
          className={cn("absolute inset-0 h-full w-full object-cover", !loaded && "invisible")} />
      )}
      {ribbon && <Ribbon kind={ribbon} mini />}
    </div>
  )
}
