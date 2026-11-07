import { audiobooks } from "@/api"
import type { ReadAlongTiming, ReadAlongUnit } from "@/api"
import type { View } from "@/vendor/foliate-js/view.js"

// ReadAlongPlayer plays a book's audiobook against its read-along edition:
// one <audio> element streams the chunks in turn, the sentence being read is
// highlighted, and the page turns to follow it. Time is "book time" — seconds
// from the start of the audiobook — which is also what the listen position
// stores, so the web, the plain player and e-readers all share one spot.

export const ACTIVE_CLASS = "booky-reading"

export interface PlayerState {
  playing: boolean
  /** book time, seconds */
  position: number
  duration: number
  rate: number
  /** the audio couldn't be loaded */
  error?: string
}

type Doc = { index: number; doc: Document }
type Renderer = { getContents: () => Doc[]; goTo: (t: { index: number; anchor: (d: Document) => Element | null }) => Promise<void> }
type Resources = { opf: Document; spine: { idref: string }[] }

const SAVE_EVERY = 15_000

export class ReadAlongPlayer {
  readonly duration: number
  private readonly units: ReadAlongUnit[]
  private readonly sectionOf = new Map<string, number>()
  private readonly audio = new Audio()
  private chunk = -1
  private pendingSeek: number | null = null
  private current = -1 // index into units
  private lit: WeakRef<Element> | null = null
  private frame = 0
  private lastSave = 0
  private lastEmit = 0
  private savedAt = -1
  private destroyed = false
  private following = Promise.resolve()

  private readonly bookId: number
  private readonly timing: ReadAlongTiming
  private readonly view: View
  private readonly onState: (s: PlayerState) => void

  constructor(bookId: number, timing: ReadAlongTiming, view: View, onState: (s: PlayerState) => void) {
    this.bookId = bookId
    this.timing = timing
    this.view = view
    this.onState = onState
    this.duration = timing.duration
    this.units = [...timing.units].sort((a, b) => a.begin - b.begin)
    this.mapSections()
    this.audio.preload = "auto"
    this.audio.addEventListener("ended", this.onEnded)
    this.audio.addEventListener("loadedmetadata", this.onLoaded)
    this.audio.addEventListener("play", this.onPlay)
    this.audio.addEventListener("pause", this.onPause)
    this.audio.addEventListener("error", this.onError)
    this.audio.addEventListener("ratechange", this.emit)
    this.setupMediaSession()
  }

  // Timing names documents the way the OPF manifest does ("text/ch1.xhtml",
  // relative to the OPF); the reader knows sections by spine position. The
  // manifest item's id links the two exactly.
  private mapSections() {
    const res = (this.view.book as unknown as { resources?: Resources }).resources
    if (!res) return
    for (const item of res.opf.getElementsByTagNameNS("*", "item")) {
      const href = item.getAttribute("href")
      const id = item.getAttribute("id")
      if (!href || !id) continue
      const index = res.spine.findIndex(s => s.idref === id)
      if (index >= 0) this.sectionOf.set(href, index)
    }
  }

  get position(): number {
    const c = this.timing.chunks[this.chunk]
    if (!c) return 0
    return c.start + (this.pendingSeek ?? this.audio.currentTime)
  }
  get playing(): boolean { return !this.audio.paused }

  private emit = () => {
    if (this.destroyed) return
    this.lastEmit = Date.now()
    this.onState({ playing: this.playing, position: this.position, duration: this.duration, rate: this.audio.playbackRate })
  }

  // ---- loading and seeking -------------------------------------------------

  private chunkAt(t: number): number {
    const cs = this.timing.chunks
    let i = cs.length - 1
    while (i > 0 && cs[i].start > t) i--
    return Math.max(0, i)
  }

  /** seek moves to book time t, loading another chunk when it needs to. */
  seek(t: number) {
    t = Math.min(Math.max(0, t), Math.max(0, this.duration - 0.25))
    const i = this.chunkAt(t)
    const c = this.timing.chunks[i]
    if (!c) return
    const local = t - c.start
    if (i !== this.chunk) {
      // swapping the source stops playback; carry on if it was playing
      const resume = this.playing
      this.chunk = i
      this.pendingSeek = local
      this.audio.src = audiobooks.readAlongUrl(this.bookId, `audio/${c.file}`)
      this.audio.load()
      if (resume) void this.audio.play().catch(this.onPlayFailed)
    } else if (this.pendingSeek !== null) {
      this.pendingSeek = local
    } else {
      this.audio.currentTime = local
    }
    this.sync(t, true)
    this.emit()
  }

  private onLoaded = () => {
    if (this.pendingSeek !== null) {
      this.audio.currentTime = this.pendingSeek
      this.pendingSeek = null
    }
    this.emit()
  }

  private onEnded = () => {
    const next = this.chunk + 1
    if (next >= this.timing.chunks.length) {
      this.save(true)
      this.emit()
      return
    }
    this.seek(this.timing.chunks[next].start)
    void this.audio.play().catch(this.onPlayFailed)
  }

  private onError = () => {
    if (this.destroyed || !this.audio.src) return
    this.onState({ playing: false, position: this.position, duration: this.duration, rate: this.audio.playbackRate,
      error: "Couldn't load the audio." })
  }
  private onPlayFailed = (e: unknown) => {
    // AbortError: a seek replaced the source mid-play; the new one plays on
    if (e instanceof DOMException && e.name === "AbortError") return
    this.onError()
  }

  // ---- play / pause ----------------------------------------------------------

  play() {
    if (this.chunk < 0) this.seek(0)
    void this.audio.play().catch(this.onPlayFailed)
  }
  pause() { this.audio.pause() }
  toggle() { if (this.playing) this.pause(); else this.play() }

  /** playFrom starts at the first sentence on the page in view. */
  playFromPage() {
    const t = this.firstOnPage()
    if (t !== null) this.seek(t)
    this.play()
  }

  setRate(rate: number) { this.audio.playbackRate = rate }

  /** skip moves by whole sentences (n may be negative). */
  skip(n: number) {
    const i = this.current >= 0 ? this.current : this.unitAt(this.position)
    const target = this.units[Math.min(Math.max(0, i + n), this.units.length - 1)]
    // back from mid-sentence restarts it, as players do
    if (n < 0 && i >= 0 && this.position - this.units[i].begin > 1.5) this.seek(this.units[i].begin)
    else if (target) this.seek(target.begin)
  }

  private onPlay = () => {
    cancelAnimationFrame(this.frame)
    const tick = () => {
      if (this.destroyed || this.audio.paused) return
      this.sync(this.position, false)
      if (Date.now() - this.lastSave > SAVE_EVERY) this.save(false)
      if (Date.now() - this.lastEmit > 250) this.emit()
      this.frame = requestAnimationFrame(tick)
    }
    this.frame = requestAnimationFrame(tick)
    this.emit()
  }
  private onPause = () => {
    cancelAnimationFrame(this.frame)
    this.save(true)
    this.emit()
  }

  // ---- following the text -----------------------------------------------------

  /** unitAt is the last sentence that has begun by t, or -1. */
  private unitAt(t: number): number {
    let lo = 0, hi = this.units.length - 1, at = -1
    while (lo <= hi) {
      const mid = (lo + hi) >> 1
      if (this.units[mid].begin <= t + 0.05) { at = mid; lo = mid + 1 } else hi = mid - 1
    }
    return at
  }

  private sync(t: number, force: boolean) {
    const i = this.unitAt(t)
    const live = i >= 0 && t <= this.units[i].end + 0.5
    if (i === this.current && !force) {
      if (!live) this.unlight()
      return
    }
    this.current = i
    this.unlight()
    if (i < 0) return
    const u = this.units[i]
    // one page turn at a time, so a burst of short sentences can't race
    this.following = this.following.then(() => this.follow(u, live)).catch(() => {})
  }

  private async follow(u: ReadAlongUnit, light: boolean) {
    if (this.destroyed || this.units[this.current] !== u) return
    const index = this.sectionOf.get(u.href)
    if (index === undefined) return
    const renderer = this.view.renderer as unknown as Renderer
    let el = this.element(index, u.id)
    if (!el || !this.onPage(el)) {
      await renderer.goTo({ index, anchor: d => d.getElementById(u.id) })
      el = this.element(index, u.id)
    }
    if (el && light && this.units[this.current] === u) {
      el.classList.add(ACTIVE_CLASS)
      this.lit = new WeakRef(el)
    }
  }

  private element(index: number, id: string): Element | null {
    const renderer = this.view.renderer as unknown as Renderer
    return renderer.getContents().find(c => c.index === index)?.doc.getElementById(id) ?? null
  }

  private onPage(el: Element): boolean {
    const range = (this.view.lastLocation as { range?: Range } | undefined)?.range
    try { return !!range && range.intersectsNode(el) } catch { return false }
  }

  private unlight() {
    this.lit?.deref()?.classList.remove(ACTIVE_CLASS)
    this.lit = null
  }

  /** firstOnPage is the book time of the first sentence on the page in view. */
  private firstOnPage(): number | null {
    const loc = this.view.lastLocation as { range?: Range; section?: { current: number } } | undefined
    const range = loc?.range
    const index = loc?.section?.current
    if (!range || index === undefined) return null
    // the current sentence is on screen: carry on from where we are
    if (this.current >= 0) {
      const el = this.element(index, this.units[this.current].id)
      if (el && this.onPage(el)) return null
    }
    for (const u of this.units) {
      if (this.sectionOf.get(u.href) !== index) continue
      const el = this.element(index, u.id)
      if (!el) continue
      try {
        if (range.intersectsNode(el)) return u.begin
        // past the page: the page itself has no narrated sentence start
        if (range.comparePoint(el, 0) > 0) return u.begin
      } catch { /* detached node */ }
    }
    return null
  }

  // ---- saving and the lock screen ------------------------------------------

  save(force: boolean) {
    const p = this.position
    if (this.chunk < 0 || (!force && Math.abs(p - this.savedAt) < 1)) return
    this.lastSave = Date.now()
    this.savedAt = p
    void audiobooks.savePosition(this.bookId, p).catch(() => {})
  }

  private setupMediaSession() {
    if (!("mediaSession" in navigator)) return
    const ms = navigator.mediaSession
    const on = (a: MediaSessionAction, h: MediaSessionActionHandler | null) => { try { ms.setActionHandler(a, h) } catch { /* unsupported */ } }
    on("play", () => this.play())
    on("pause", () => this.pause())
    on("seekbackward", d => this.seek(this.position - (d.seekOffset ?? 15)))
    on("seekforward", d => this.seek(this.position + (d.seekOffset ?? 30)))
    on("previoustrack", () => this.skip(-1))
    on("nexttrack", () => this.skip(1))
    on("seekto", d => { if (d.seekTime !== undefined) this.seek(d.seekTime) })
  }
  setMetadata(title: string, artist: string) {
    if ("mediaSession" in navigator && "MediaMetadata" in window)
      navigator.mediaSession.metadata = new MediaMetadata({ title, artist })
  }

  destroy() {
    if (this.destroyed) return
    this.save(true)
    this.destroyed = true
    cancelAnimationFrame(this.frame)
    this.unlight()
    this.audio.pause()
    this.audio.removeAttribute("src")
    this.audio.load()
    if ("mediaSession" in navigator) {
      for (const a of ["play", "pause", "seekbackward", "seekforward", "previoustrack", "nexttrack", "seekto"] as MediaSessionAction[]) {
        try { navigator.mediaSession.setActionHandler(a, null) } catch { /* unsupported */ }
      }
      navigator.mediaSession.metadata = null
    }
  }
}
