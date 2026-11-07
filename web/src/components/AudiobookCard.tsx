import { useEffect, useRef, useState } from "react"
import { BookAudio, FolderInput, Headphones, Loader2, MoreVertical, RefreshCw, Trash2, Upload } from "lucide-react"
import { toast } from "sonner"
import { audiobooks } from "@/api"
import type { ApiAudiobook, ApiBook } from "@/api"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { PathInput } from "@/components/PathInput"
import { useIsAdmin } from "@/lib/access"
import { cn } from "@/lib/utils"
import { formatDuration } from "@/lib/time"

// The audiobook on a book's page: add one (upload, or a path on the server
// for admins), watch Booky line it up with the text, then listen or read
// along. It sits beside the ebook on disk, under the same name.

// estimate says how long a build has left, from how fast it has gone since
// the page first saw it — a few minutes per hour of audio on most servers.
function estimate(start: { at: number; progress: number } | null, progress: number): string {
  const base = "Runs in the background — you can leave this page."
  if (!start || progress - start.progress < 0.03) return base + " Usually a few minutes per hour of audio."
  const rate = (progress - start.progress) / (Date.now() - start.at) // share per ms
  const left = Math.round((1 - progress) / rate / 60000)
  if (left < 1) return base + " Almost done."
  return base + ` About ${left} min left.`
}

// upload with progress — audiobooks run to hundreds of megabytes, and a bar
// that sits at zero for two minutes reads as broken
function uploadWithProgress(bookId: number, files: File[], onProgress: (f: number) => void): Promise<void> {
  return new Promise((resolve, reject) => {
    const form = new FormData()
    for (const f of files) form.append("file", f)
    const xhr = new XMLHttpRequest()
    xhr.open("POST", `/api/v1/books/${bookId}/audiobook`)
    xhr.upload.onprogress = e => { if (e.lengthComputable) onProgress(e.loaded / e.total) }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) return resolve()
      let msg = `${xhr.status}`
      try { msg = JSON.parse(xhr.responseText).error ?? msg } catch { /* not json */ }
      reject(new Error(msg))
    }
    xhr.onerror = () => reject(new Error("upload failed — check the connection"))
    xhr.send(form)
  })
}

export function AudiobookCard({ book, onReadAlong, onChanged }: {
  book: ApiBook
  onReadAlong: (b: ApiBook) => void
  /** the audiobook's state moved on; the page refreshes what it shows */
  onChanged?: () => void
}) {
  const isAdmin = useIsAdmin()
  const [ab, setAb] = useState<ApiAudiobook | null | undefined>(undefined)
  const [uploading, setUploading] = useState<number | null>(null)
  const [pathOpen, setPathOpen] = useState(false)
  const [path, setPath] = useState("")
  const [listening, setListening] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)

  // the status last passed up to the page; a ref, so a poll scheduled by
  // an older render compares against the latest, and each change is
  // reported once
  const reported = useRef(book.audio ?? "")
  // when the build was first seen, and how far along, for an estimate
  const [started, setStarted] = useState<{ at: number; progress: number } | null>(null)
  const load = async () => {
    let next: ApiAudiobook | null = null
    try { next = await audiobooks.get(book.id) } catch { /* shown as none */ }
    setAb(next)
    const status = next?.status ?? ""
    if (status !== reported.current) {
      reported.current = status
      onChanged?.()
    }
    if (status === "working") {
      const progress = next?.progress ?? 0
      setStarted(s => s ?? { at: Date.now(), progress })
    } else {
      setStarted(null)
    }
  }
  useEffect(() => { void load() }, [book.id]) // eslint-disable-line react-hooks/exhaustive-deps

  // keep watching while something is happening in the background
  const busy = !!ab && (ab.importing || ab.status === "queued" || ab.status === "working")
  useEffect(() => {
    if (!busy) return
    const t = setInterval(() => void load(), 3000)
    return () => clearInterval(t)
  }, [busy, book.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const upload = async (files: File[]) => {
    if (files.length === 0) return
    setUploading(0)
    try {
      await uploadWithProgress(book.id, files, setUploading)
      toast.success(files.length > 1
        ? `${files.length} files uploaded — joining them into one audiobook`
        : "Audiobook uploaded")
    } catch (e) {
      toast.error(`Upload failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setUploading(null)
      void load()
    }
  }
  const importPath = async () => {
    try {
      await audiobooks.importPath(book.id, path.trim())
      setPathOpen(false)
      setPath("")
      toast.success("Adding the audiobook")
    } catch (e) {
      toast.error(`Import failed: ${e instanceof Error ? e.message : e}`)
    }
    void load()
  }
  const rebuild = async () => {
    try { await audiobooks.rebuild(book.id); toast("Lining it up again") } catch (e) { toast.error(`${e instanceof Error ? e.message : e}`) }
    void load()
  }
  const remove = async (deleteFile: boolean) => {
    const what = deleteFile ? "Remove the audiobook and delete its file from disk?" : "Remove the audiobook from Booky? The file stays on disk."
    if (!window.confirm(what)) return
    try { await audiobooks.remove(book.id, deleteFile); toast("Audiobook removed") } catch (e) { toast.error(`${e instanceof Error ? e.message : e}`) }
    setListening(false)
    void load()
  }

  if (ab === undefined) return null
  const picker = (
    <input ref={fileInput} type="file" multiple className="hidden"
      accept=".mp3,.m4b,.m4a,audio/mpeg,audio/mp4"
      onChange={e => { void upload([...(e.target.files ?? [])]); e.target.value = "" }} />
  )
  const addButtons = (
    <div className="mt-3 flex flex-wrap items-center gap-2">
      <Button variant="outline" className="h-9" disabled={uploading !== null} onClick={() => fileInput.current?.click()}>
        <Upload className="mr-1.5 h-4 w-4" /> {ab ? "Replace audiobook" : "Add audiobook"}
      </Button>
      {isAdmin && (
        <Button variant="ghost" className="h-9 text-muted-foreground" onClick={() => setPathOpen(o => !o)}>
          <FolderInput className="mr-1.5 h-4 w-4" /> From the server…
        </Button>
      )}
      {picker}
    </div>
  )

  return (
    <div className="mt-5 rounded-2xl border border-linesoft bg-surface px-5 py-4">
      <div className="flex items-center gap-2">
        <Headphones className="h-4 w-4 text-muted-foreground" />
        <h2 className="text-[13.5px] font-semibold">Audiobook</h2>
        {ab?.duration ? <span className="mono-label text-faint">{formatDuration(ab.duration)}</span> : null}
        {isAdmin && ab?.status && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button aria-label="Audiobook options" className="ml-auto flex h-7 w-7 items-center justify-center rounded-lg text-muted-foreground hover:bg-surface2 hover:text-foreground">
                <MoreVertical className="h-4 w-4" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-[210px] rounded-xl">
              <DropdownMenuItem onClick={() => fileInput.current?.click()}>
                <Upload className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Replace audiobook
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => void rebuild()}>
                <RefreshCw className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Line up again
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => void remove(false)}>
                <Trash2 className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Remove, keep file
              </DropdownMenuItem>
              <DropdownMenuItem className="text-want" onClick={() => void remove(true)}>
                <Trash2 className="mr-2 h-3.5 w-3.5" /> Remove & delete file
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>

      {uploading !== null && <Progress label={`Uploading… ${Math.round(uploading * 100)}%`} value={uploading} />}

      {uploading === null && ab?.importing && (
        <Progress label="Putting the audiobook in place — joining parts can take a few minutes" indeterminate />
      )}
      {uploading === null && ab?.importError && (
        <p className="mt-2 text-[12.5px] text-want">{ab.importError}</p>
      )}

      {!ab?.status && uploading === null && !ab?.importing && (
        <>
          <p className="mt-2 max-w-[62ch] text-[12.5px] text-muted-foreground">
            Add the audiobook — one file, or all its parts. It's saved next to the ebook, and Booky lines it
            up with the text so you can read along while it plays.
          </p>
          {addButtons}
        </>
      )}

      {ab?.status === "queued" && !ab.importing && (
        <Progress label="Waiting its turn to be lined up with the text" indeterminate />
      )}
      {ab?.status === "working" && (
        <Progress label={`${ab.phase || "Lining up with the text"} · ${Math.round((ab.progress ?? 0) * 100)}%`}
          value={ab.progress ?? 0} hint={estimate(started, ab.progress ?? 0)} />
      )}
      {ab?.status === "failed" && (
        <div className="mt-2">
          <p className="text-[12.5px] text-want">{ab.error || "Couldn't line it up with the text."}</p>
          {isAdmin && (
            <Button variant="outline" className="mt-2 h-8" onClick={() => void rebuild()}>
              <RefreshCw className="mr-1.5 h-3.5 w-3.5" /> Try again
            </Button>
          )}
        </div>
      )}

      {ab?.status && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {ab.readAlong && (
            <Button className="h-9 px-4 font-semibold" onClick={() => onReadAlong(book)}>
              <BookAudio className="mr-1.5 h-4 w-4" /> Read along
            </Button>
          )}
          <Button variant="outline" className="h-9" onClick={() => setListening(l => !l)}>
            <Headphones className="mr-1.5 h-4 w-4" /> {listening ? "Hide player" : "Just listen"}
          </Button>
          {ab.readAlong && (
            <p className="basis-full text-[11.5px] text-faint">
              Read along opens the book with the narration and follows it sentence by sentence. Just listen plays the audiobook here on its own. Both pick up where you left off.
            </p>
          )}
          {ab.status === "ready" && ab.match ? (
            <span className="mono-label text-faint" title="How much of the narration matched the text">
              {Math.round(ab.match * 100)}% matched
            </span>
          ) : null}
          {picker}
        </div>
      )}
      {ab?.status && listening && <ListenPlayer bookId={book.id} />}

      {pathOpen && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <PathInput value={path} onChange={setPath} placeholder="/data/downloads/… (a file, or a folder of parts)" className="min-w-[280px] flex-1" />
          <Button className="h-9" disabled={!path.trim()} onClick={() => void importPath()}>Import</Button>
        </div>
      )}
    </div>
  )
}

function Progress({ label, value, indeterminate, hint }: { label: string; value?: number; indeterminate?: boolean; hint?: string }) {
  return (
    <div className="mt-3">
      <div className="flex items-center gap-2 text-[12.5px] text-muted-foreground">
        <Loader2 className="h-3.5 w-3.5 animate-spin" /> {label}
      </div>
      <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-surface3">
        <div className={cn("h-full rounded-full bg-brass transition-[width] duration-500", indeterminate && "w-1/3 animate-pulse")}
          style={indeterminate ? undefined : { width: `${Math.max(2, Math.round((value ?? 0) * 100))}%` }} />
      </div>
      {hint && <p className="mt-1.5 text-[11.5px] text-faint">{hint}</p>}
    </div>
  )
}

// ListenPlayer plays the audiobook itself, picking up where this account
// left off and saving the spot as it goes — the same position the
// read-along and e-readers use.
function ListenPlayer({ bookId }: { bookId: number }) {
  const audio = useRef<HTMLAudioElement>(null)
  const lastSaved = useRef(0)
  useEffect(() => {
    let cancelled = false
    void audiobooks.position(bookId).then(p => {
      if (!cancelled && audio.current && p.position > 0) audio.current.currentTime = p.position
    }).catch(() => {})
    const el = audio.current
    const save = () => {
      if (!el || !Number.isFinite(el.currentTime)) return
      lastSaved.current = Date.now()
      void audiobooks.savePosition(bookId, el.currentTime).catch(() => {})
    }
    const onTime = () => { if (Date.now() - lastSaved.current > 10_000) save() }
    el?.addEventListener("timeupdate", onTime)
    el?.addEventListener("pause", save)
    return () => {
      cancelled = true
      el?.removeEventListener("timeupdate", onTime)
      el?.removeEventListener("pause", save)
      if (el && el.currentTime > 0) save()
    }
  }, [bookId])
  return (
    // eslint-disable-next-line jsx-a11y/media-has-caption -- an audiobook; the text is the book
    <audio ref={audio} controls preload="metadata" className="mt-3 w-full" src={audiobooks.fileUrl(bookId)} />
  )
}
