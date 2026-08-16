import { useEffect, useState } from "react"
import { acquisition, api, canRead, coverUrl, kindle } from "@/api"
import type { ApiBook, ApiKindleDevice, ApiLibrary } from "@/api"
import { AddToLibraryButton } from "@/components/AddToLibrary"
import { Cover, hashColors } from "@/components/Cover"
import { Tag, Fmt } from "@/components/bits"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { ArrowLeft, BookOpenText, FolderInput, Lock, LockOpen, Mail, Pencil, RefreshCw, Search, Trash2, Upload, Zap } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { useIsAdmin } from "@/lib/access"
import { removeModesFor, removeModeDesc, removeModeLabel } from "@/lib/removeModes"
import type { RemoveMode } from "@/lib/removeModes"
import { bookRibbon } from "@/views/Library"
import { ReleasesDialog } from "@/components/ReleasesDialog"
import { PathInput } from "@/components/PathInput"
import { useApi } from "@/hooks/use-api"

// ManualImportDialog delivers a file already on disk into a library for a
// book — same renaming, metadata write, and hardlinking as automatic
// imports. Reused by the book page and by failed rows in Activity (which
// pass the failed queueId so a successful import resolves the row, and the
// downloaded file's path as the starting point).
export function ManualImportDialog({ open, onOpenChange, bookId, bookTitle, defaultLibraryId, initialPath, queueId, onImported }: {
  open: boolean
  onOpenChange: (v: boolean) => void
  bookId: number
  bookTitle: string
  defaultLibraryId?: number
  initialPath?: string
  queueId?: number
  onImported: () => void
}) {
  const { data } = useApi(() => api.libraries())
  const libraries = data?.libraries ?? []
  const [path, setPath] = useState("")
  const [file, setFile] = useState<File | null>(null)
  const [libraryId, setLibraryId] = useState(0)
  const [importing, setImporting] = useState(false)
  const targetLib = libraryId || defaultLibraryId || libraries[0]?.id || 0
  useEffect(() => {
    if (open) { setPath(initialPath ?? ""); setFile(null) }
  }, [open, initialPath])

  const run = async () => {
    if (!targetLib || (!path.trim() && !file)) return
    setImporting(true)
    try {
      const r = file
        ? await api.manualImportUpload(bookId, targetLib, file, queueId)
        : await api.manualImport(bookId, targetLib, path.trim(), queueId)
      toast.success(`Imported → ${r.path}`)
      onImported()
      onOpenChange(false)
      setPath("")
      setFile(null)
    } catch (e) {
      toast.error(`Import failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setImporting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">Manual import · {bookTitle}</DialogTitle>
          <p className="text-xs text-muted-foreground">
            Point at a book file (or a folder containing one) under /data. It's renamed by your
            naming scheme, metadata is written in, and it lands in the library — the source file is moved.
          </p>
        </DialogHeader>
        <div className="grid gap-4 py-1">
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">File or folder on the server</div>
            <PathInput value={path} onChange={setPath} placeholder="/data/downloads/…" />
            <div className="mt-2 flex items-center gap-2">
              <label className="inline-flex cursor-pointer items-center gap-1.5 text-[12.5px] text-muted-foreground hover:text-foreground">
                <Upload className="h-3.5 w-3.5" /> …or upload from this device
                <input type="file" accept=".epub,.azw3,.azw,.mobi,.pdf,.cbz,.cbr,.fb2" className="hidden"
                  onChange={e => { setFile(e.target.files?.[0] ?? null); setPath(""); e.target.value = "" }} />
              </label>
              {file && (
                <span className="flex items-center gap-1.5 rounded-lg border border-brass/40 bg-brass/10 px-2 py-0.5 text-[12px] text-brass">
                  {file.name}
                  <button type="button" onClick={() => setFile(null)} className="opacity-60 hover:opacity-100">×</button>
                </span>
              )}
            </div>
          </div>
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">Into library</div>
            <select className="h-9 rounded-md border border-input bg-transparent px-2.5 text-[12.5px]"
              value={targetLib} onChange={e => setLibraryId(Number(e.target.value))}>
              {libraries.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}
            </select>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button disabled={importing || (!path.trim() && !file) || !targetLib} onClick={run}>
            {importing ? "Importing…" : "Import"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function EditMetadataDialog({ open, onOpenChange, book, onSaved }: {
  open: boolean
  onOpenChange: (v: boolean) => void
  book: ApiBook
  onSaved: () => void
}) {
  const [title, setTitle] = useState(book.title)
  const [author, setAuthor] = useState(book.author)
  const [description, setDescription] = useState(book.description ?? "")
  const [publisher, setPublisher] = useState(book.publisher ?? "")
  const [language, setLanguage] = useState(book.language ?? "")
  const [releaseDate, setReleaseDate] = useState(book.releaseDate ?? "")
  const [seriesName, setSeriesName] = useState(book.seriesName ?? "")
  const [seriesNum, setSeriesNum] = useState(book.seriesNum ? String(book.seriesNum) : "")
  const [genres, setGenres] = useState((book.genres ?? []).join(", "))
  const [isbn13, setIsbn13] = useState(book.isbn13 ?? "")
  const [hardcoverId, setHardcoverId] = useState(book.hardcoverId ?? "")
  // per-field refresh-protection locks; fetched fresh because list responses
  // don't carry them
  const [locks, setLocks] = useState<Record<string, boolean>>({})
  // existing series/author names feed the datalists so the fields are
  // type-anything AND pick-from-known
  const [seriesOptions, setSeriesOptions] = useState<string[]>([])
  const [authorOptions, setAuthorOptions] = useState<string[]>([])
  const [coverUrlDraft, setCoverUrlDraft] = useState("")
  const [coverBust, setCoverBust] = useState(0)
  useEffect(() => {
    if (!open) return
    api.book(book.id).then(b => setLocks(b.fieldLocks ?? {})).catch(() => setLocks({}))
    api.series().then(r => setSeriesOptions((r.series ?? []).map(s => s.name).sort())).catch(() => setSeriesOptions([]))
    api.authors().then(r => setAuthorOptions((r.authors ?? []).map(a => a.name).sort())).catch(() => setAuthorOptions([]))
  }, [open, book.id])

  const applyCoverUrl = async () => {
    const url = coverUrlDraft.trim()
    if (!url) return
    try {
      await api.setCoverUrl(book.id, url)
      setCoverUrlDraft("")
      setCoverBust(Date.now())
      setLocks(prev => ({ ...prev, cover: true }))
      toast.success("Cover replaced and locked — refreshes won't touch it")
    } catch (e) {
      toast.error(`Couldn't fetch that image: ${e instanceof Error ? e.message : e}`)
    }
  }
  const applyCoverFile = async (file: File | undefined) => {
    if (!file) return
    try {
      await api.uploadCover(book.id, file)
      setCoverBust(Date.now())
      setLocks(prev => ({ ...prev, cover: true }))
      toast.success("Cover replaced and locked — refreshes won't touch it")
    } catch (e) {
      toast.error(`Couldn't upload: ${e instanceof Error ? e.message : e}`)
    }
  }

  const toggleLock = async (field: string) => {
    const next = !locks[field]
    try {
      const r = await api.setFieldLock(book.id, field, next)
      setLocks(r.fieldLocks ?? {})
      toast.success(next ? "Locked — refreshes won't touch this field" : "Unlocked — refreshes may update this field")
    } catch (e) {
      toast.error(`Couldn't change lock: ${e instanceof Error ? e.message : e}`)
    }
  }

  const save = async () => {
    const fields: Record<string, string> = {}
    if (title !== book.title) fields.title = title
    if (author.trim() && author !== book.author) fields.author = author.trim()
    if (description !== (book.description ?? "")) fields.description = description
    if (publisher !== (book.publisher ?? "")) fields.publisher = publisher
    if (language !== (book.language ?? "")) fields.language = language
    if (releaseDate !== (book.releaseDate ?? "")) fields.releaseDate = releaseDate
    if (seriesName !== (book.seriesName ?? "")) fields.seriesName = seriesName
    const numOrig = book.seriesNum ? String(book.seriesNum) : ""
    if (seriesNum !== numOrig) fields.seriesNum = seriesNum
    if (genres !== (book.genres ?? []).join(", ")) fields.genres = genres
    if (isbn13 !== (book.isbn13 ?? "")) fields.isbn13 = isbn13
    if (hardcoverId !== (book.hardcoverId ?? "")) fields.hardcoverId = hardcoverId.trim()
    if (Object.keys(fields).length === 0) { onOpenChange(false); return }
    try {
      await api.editBook(book.id, fields, true)
      toast.success("Saved — edited fields are locked against refreshes")
      onSaved()
      onOpenChange(false)
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }

  // lockKey groups seriesName+seriesNum under the single "series" lock the
  // backend uses. No lockKey = the field has no lock (never overwritten).
  const Field = ({ label, lockKey, children }: { label: string; lockKey?: string; children: React.ReactNode }) => (
    <div>
      <div className="mono-label mb-1.5 flex items-center gap-1.5 text-muted-foreground">
        {label}
        {lockKey && (
          <button type="button" onClick={() => void toggleLock(lockKey)}
            aria-label={locks[lockKey] ? `Unlock ${label}` : `Lock ${label}`}
            title={locks[lockKey] ? "Locked — refreshes won't touch this field" : "Unlocked — click to protect from refreshes"}
            className={locks[lockKey] ? "text-brass" : "text-faint hover:text-muted-foreground"}>
            {locks[lockKey] ? <Lock className="h-3 w-3" /> : <LockOpen className="h-3 w-3" />}
          </button>
        )}
      </div>
      {children}
    </div>
  )
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">Edit metadata · {book.title}</DialogTitle>
          <p className="text-xs text-muted-foreground">Edited fields lock automatically — provider refreshes never overwrite them.</p>
        </DialogHeader>
        <div className="grid gap-4 py-1">
          <Field label="Title" lockKey="title"><Input value={title} onChange={e => setTitle(e.target.value)} /></Field>
          <Field label="Author" lockKey="author">
            <Input value={author} onChange={e => setAuthor(e.target.value)} list="edit-author-options" />
            <datalist id="edit-author-options">
              {authorOptions.map(a => <option key={a} value={a} />)}
            </datalist>
            <p className="mt-1 text-xs text-faint">A name that isn't in Booky yet creates that author. The book moves to their page, taking its series with it.</p>
          </Field>
          <div className="grid grid-cols-[1fr_90px] gap-3">
            <Field label="Series" lockKey="series">
              <Input value={seriesName} onChange={e => setSeriesName(e.target.value)} placeholder="none"
                list="edit-series-options" />
              <datalist id="edit-series-options">
                {seriesOptions.map(s => <option key={s} value={s} />)}
              </datalist>
            </Field>
            <Field label="Series #"><Input value={seriesNum} onChange={e => setSeriesNum(e.target.value)} inputMode="decimal" /></Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Publisher" lockKey="publisher"><Input value={publisher} onChange={e => setPublisher(e.target.value)} /></Field>
            <Field label="Language" lockKey="language"><Input value={language} onChange={e => setLanguage(e.target.value)} /></Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Release date" lockKey="releaseDate"><Input value={releaseDate} onChange={e => setReleaseDate(e.target.value)} placeholder="YYYY-MM-DD" /></Field>
            <Field label="ISBN-13" lockKey="isbn13"><Input value={isbn13} onChange={e => setIsbn13(e.target.value)} /></Field>
          </div>
          <Field label="Hardcover ID" lockKey="hardcoverId">
            <Input value={hardcoverId} onChange={e => setHardcoverId(e.target.value)} placeholder="none" inputMode="numeric" />
            <p className="mt-1 text-xs text-faint">
              The canonical Hardcover identity — Refresh metadata pulls this exact book. Paste a
              known id to re-link a wrong match, or clear it to let the next refresh re-match by
              title and author (lock the empty field to stop matching entirely).
            </p>
          </Field>
          <Field label="Genres" lockKey="genres"><Input value={genres} onChange={e => setGenres(e.target.value)} placeholder="Thriller, Mystery, …" /></Field>
          <Field label="Description" lockKey="description"><Textarea value={description} onChange={e => setDescription(e.target.value)} rows={4} className="text-[13px]" /></Field>
          <Field label="Cover" lockKey="cover">
            <div className="flex items-start gap-3">
              <img src={coverUrl(book.id) + (coverBust ? `?v=${coverBust}` : "")} alt=""
                onError={e => { (e.target as HTMLImageElement).style.visibility = "hidden" }}
                className="aspect-2/3 w-[64px] shrink-0 rounded-[3px_6px_6px_3px] border border-linesoft object-cover" />
              <div className="min-w-0 flex-1">
                <div className="flex gap-2">
                  <Input value={coverUrlDraft} onChange={e => setCoverUrlDraft(e.target.value)}
                    placeholder="https:// — image URL" className="h-8 text-[12.5px]"
                    onKeyDown={e => { if (e.key === "Enter") void applyCoverUrl() }} />
                  <Button variant="outline" className="h-8 shrink-0" disabled={!coverUrlDraft.trim()} onClick={applyCoverUrl}>Fetch</Button>
                </div>
                <label className="mt-2 inline-flex cursor-pointer items-center gap-1.5 text-[12.5px] text-muted-foreground hover:text-foreground">
                  <Upload className="h-3.5 w-3.5" /> Upload an image…
                  <input type="file" accept="image/*" className="hidden"
                    onChange={e => { void applyCoverFile(e.target.files?.[0]); e.target.value = "" }} />
                </label>
                <p className="mt-1.5 text-xs text-faint">A cover you set here is locked automatically — Regenerate cover and refreshes leave it alone until you unlock it.</p>
              </div>
            </div>
          </Field>
        </div>
        <DialogFooter>
          <Button variant="outline" className="" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button className="" onClick={save}>Save</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function BookDetail({ book: initial, onBack, onChanged, onRead, onOpenSeries, onOpenAuthor }: {
  book: ApiBook
  onBack: () => void
  onChanged: () => void
  onRead?: (b: ApiBook) => void
  onOpenSeries?: (b: ApiBook) => void
  onOpenAuthor?: (b: ApiBook) => void
}) {
  const isAdmin = useIsAdmin()
  const [book, setBook] = useState(initial)
  const [editOpen, setEditOpen] = useState(false)
  const [searchOpen, setSearchOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [autoGrabbing, setAutoGrabbing] = useState(false)
  const [c1, c2] = hashColors(book.title)

  // Refresh/re-match responses carry the book's primary library presence; if
  // this page was opened from a different library, keep its context so the
  // file/monitor display never flips to another library's state.
  const withContext = (next: ApiBook): ApiBook =>
    book.libraryId && next.libraryId !== book.libraryId
      ? {
        ...next, libraryId: book.libraryId, monitored: book.monitored,
        filePath: book.filePath, fileFormat: book.fileFormat, fileSize: book.fileSize,
      }
      : next

  const remove = async (mode: RemoveMode) => {
    if (!book.libraryId) return
    if (!window.confirm(`${book.title}\n\n${removeModeLabel(mode)}?\n${removeModeDesc(mode)}`)) return
    try {
      await api.removeBook(book.libraryId, book.id, mode)
      toast(`${book.title} removed`)
      onChanged()
      onBack()
    } catch (e) {
      toast.error(`Couldn't remove: ${e instanceof Error ? e.message : e}`)
    }
  }

  const refresh = async () => {
    toast("Refreshing metadata…")
    try {
      setBook(withContext(await api.refreshBook(book.id)))
      toast.success("Metadata refreshed — locked fields untouched")
    } catch (e) {
      toast.error(`Refresh failed: ${e instanceof Error ? e.message : e}`)
    }
    onChanged()
  }

  const autoGrab = async () => {
    if (!book.libraryId) { toast.error("Monitor this book into a library first"); return }
    setAutoGrabbing(true)
    try {
      const r = await acquisition.autoGrab(book.id, book.libraryId)
      if (r.grabbed) {
        toast.success("Grabbed the top-ranked release — watch Activity")
        onChanged()
      } else {
        toast.warning("Nothing grabbable — every candidate failed or scored zero")
      }
    } catch (e) {
      toast.error(`Auto-grab failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setAutoGrabbing(false)
    }
  }

  const toggleMonitored = async (v: boolean) => {
    if (!book.libraryId) return
    try {
      await api.setBookMonitored(book.libraryId, book.id, v)
      setBook({ ...book, monitored: v })
      onChanged()
    } catch (e) {
      toast.error(`Couldn't update: ${e instanceof Error ? e.message : e}`)
    }
  }

  // Send to Kindle: the button appears only when everything is ready — the
  // caller has a device covering this library whose owner email is set, and
  // the file is a format Amazon takes by email. Otherwise the action row
  // looks exactly as it always did.
  const { data: kindleData } = useApi(() => kindle.devices())
  const [sending, setSending] = useState(false)
  const bookLibraryId = book.libraryId ?? 0
  const kindleTargets = (kindleData?.devices ?? []).filter(d =>
    d.emailConfigured && bookLibraryId > 0 && d.libraryIds.includes(bookLibraryId))
  const kindleSendable = !!book.filePath && ["epub", "pdf"].includes((book.fileFormat ?? "").toLowerCase())
  const sendToKindle = async (d: ApiKindleDevice) => {
    setSending(true)
    toast(`Sending to ${d.name}…`)
    try {
      await kindle.sendBook(book.id, d.id)
      toast.success(`${book.title} → ${d.name} — it lands in the Kindle's Docs`)
    } catch (e) {
      toast.error(`Send failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setSending(false)
    }
  }

  // Monitoring a catalog-only book is what puts it in a library (same as the
  // author/series pages) — so a book that isn't shelved yet gets a Monitor
  // button naming its destination instead of no control at all.
  const { data: libData } = useApi(() => api.libraries())
  const libraries = libData?.libraries ?? []
  const [monitoring, setMonitoring] = useState(false)
  const monitorInto = async (l: ApiLibrary) => {
    setMonitoring(true)
    try {
      await api.setBookMonitored(l.id, book.id, true)
      setBook({ ...book, libraryId: l.id, monitored: true })
      toast.success(`${book.title} added to ${l.name} — monitored`)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't monitor: ${e instanceof Error ? e.message : e}`)
    } finally {
      setMonitoring(false)
    }
  }

  return (
    <section>
      <button onClick={onBack} className="mono-label mb-5 flex items-center gap-1.5 text-muted-foreground hover:text-brass">
        <ArrowLeft className="h-3.5 w-3.5" /> Library
      </button>

      {/* phones stack title above the action row so neither squeezes the
          other; side-by-side returns at sm */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between sm:gap-5">
        <div className="min-w-0">
          <div className="mono-label text-faint">
            {book.seriesName && (
              <>
                {onOpenSeries
                  ? <button className="hover:text-brass hover:underline" onClick={() => onOpenSeries(book)}>
                      {book.seriesName} #{book.seriesNum}
                    </button>
                  : `${book.seriesName} #${book.seriesNum}`}
                {" · "}
              </>
            )}
            {book.releaseDate?.slice(0, 4) ?? ""}
          </div>
          <h1 className="font-book mt-1 text-[26px] font-bold leading-tight tracking-tight text-balance sm:text-[32px]">{book.title}</h1>
          <div className="mt-1 text-[15px] text-muted-foreground">
            {onOpenAuthor
              ? <button className="hover:text-brass hover:underline" onClick={() => onOpenAuthor(book)}>{book.author}</button>
              : book.author}
          </div>
        </div>
        <TooltipProvider delayDuration={150}>
          <div className="flex shrink-0 gap-2 sm:pt-1.5">
            {/* reading is the one consumer action here — everything else in
                this row is librarian work, so Read leads and gets a label */}
            {onRead && canRead(book) && (
              <Button className="h-9 px-3.5 font-semibold" aria-label={`Read ${book.title}`}
                onClick={() => onRead(book)}>
                <BookOpenText className="mr-1.5 h-4 w-4" /> Read
              </Button>
            )}
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Auto-grab best release"
                  disabled={autoGrabbing} onClick={autoGrab}>
                  <Zap className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Auto-grab the best release</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Search releases"
                  onClick={() => {
                    if (!book.libraryId) { toast.error("Monitor this book into a library first"); return }
                    setSearchOpen(true)
                  }}>
                  <Search className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Search releases</TooltipContent>
            </Tooltip>
            {isAdmin && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Manual import"
                    onClick={() => setImportOpen(true)}>
                    <FolderInput className="h-4 w-4" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Manual import — deliver a file you already have</TooltipContent>
              </Tooltip>
            )}
            {kindleSendable && kindleTargets.length === 1 && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Send to Kindle"
                    disabled={sending} onClick={() => void sendToKindle(kindleTargets[0])}>
                    <Mail className="h-4 w-4" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Send to Kindle — {kindleTargets[0].name}</TooltipContent>
              </Tooltip>
            )}
            {kindleSendable && kindleTargets.length > 1 && (
              <DropdownMenu>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <DropdownMenuTrigger asChild>
                      <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Send to Kindle" disabled={sending}>
                        <Mail className="h-4 w-4" />
                      </Button>
                    </DropdownMenuTrigger>
                  </TooltipTrigger>
                  <TooltipContent>Send to Kindle</TooltipContent>
                </Tooltip>
                <DropdownMenuContent align="end" className="rounded-xl">
                  {kindleTargets.map(d => (
                    <DropdownMenuItem key={d.id} onClick={() => void sendToKindle(d)}>
                      {d.name} <span className="font-label ml-2 text-[10.5px] text-muted-foreground">{d.email}</span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Refresh metadata" onClick={refresh}>
                  <RefreshCw className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Refresh from Hardcover — matches &amp; adopts title, cover, series</TooltipContent>
            </Tooltip>
            {/* editing the stored metadata and deleting books are the two
                things a scoped user doesn't get — refreshing FROM the
                providers, above, they do */}
            {isAdmin && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Edit metadata" onClick={() => setEditOpen(true)}>
                    <Pencil className="h-4 w-4" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Edit metadata</TooltipContent>
              </Tooltip>
            )}
            <DropdownMenu>
              <Tooltip>
                <TooltipTrigger asChild>
                  <DropdownMenuTrigger asChild>
                    <Button variant="outline" size="icon" className="h-9 w-9 text-want hover:border-want hover:text-want"
                      aria-label="Delete book" disabled={!book.libraryId}>
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </DropdownMenuTrigger>
                </TooltipTrigger>
                <TooltipContent>Delete book</TooltipContent>
              </Tooltip>
              <DropdownMenuContent align="end">
                {removeModesFor(isAdmin).map(m => (
                  <DropdownMenuItem key={m.mode} title={m.desc}
                    onClick={() => remove(m.mode)} className="text-want">{m.label}</DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </TooltipProvider>
      </div>

      <div className="mt-6 flex flex-col gap-7 md:flex-row">
        <div className="w-[170px] shrink-0">
          <Cover c1={c1} c2={c2} title={book.title} author={book.author}
            src={coverUrl(book.id)} ribbon={bookRibbon(book)} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            {book.filePath && <Tag kind="good">On shelf</Tag>}
            {!book.filePath && book.monitored && <Tag kind="want">Missing</Tag>}
            {book.fileFormat && <Fmt>{book.fileFormat.toUpperCase()}</Fmt>}
            {book.libraryId ? (
              <span className="ml-2 flex items-center gap-2 text-xs text-muted-foreground">
                Monitored <Switch checked={book.monitored} onCheckedChange={toggleMonitored} />
              </span>
            ) : (
              <AddToLibraryButton libraries={libraries} label="Monitor" className="ml-2 h-8"
                onPick={monitorInto} busy={monitoring} />
            )}
          </div>
          <p className="mt-4 max-w-[62ch] text-[13.5px] leading-relaxed text-muted-foreground">
            {book.description || "No description cached yet — it arrives with the next metadata refresh."}
          </p>
          {(book.genres?.length ?? 0) > 0 && (
            <div className="mt-4 flex flex-wrap gap-1.5">
              {book.genres!.map(g => <Tag key={g} kind="dim">{g}</Tag>)}
            </div>
          )}
          <div className="mono-label mt-5 flex flex-wrap gap-x-5 gap-y-1 text-faint">
            {(book.ratingsCount ?? 0) > 0 && <span>{book.ratingsCount!.toLocaleString()} ratings</span>}
            {book.isbn13 && <span>isbn {book.isbn13}</span>}
            {book.goodreadsId && <span>goodreads {book.goodreadsId}</span>}
            {book.hardcoverId && <span>hardcover {book.hardcoverId}</span>}
            {book.publisher && <span>{book.publisher}</span>}
          </div>
        </div>
      </div>

      {book.filePath && (
        <>
          <div className="mono-label mb-2 mt-9 text-faint">File</div>
          <div className="border-t">
            {/* the file row opens the reader too — you're looking at the
                file, tapping it should open it */}
            <div
              className={cn("flex flex-wrap items-center gap-x-5 gap-y-1 border-b border-linesoft px-1 py-3",
                onRead && canRead(book) && "cursor-pointer hover:bg-surface2/60")}
              {...(onRead && canRead(book) ? {
                role: "button", tabIndex: 0,
                onClick: () => onRead(book),
                onKeyDown: (e: React.KeyboardEvent) => { if (e.key === "Enter") onRead(book) },
              } : {})}>
              <span className="font-label min-w-0 flex-1 truncate text-[11.5px] text-muted-foreground">{book.filePath}</span>
              <span className="text-xs text-muted-foreground">
                {book.fileFormat && <Fmt>{book.fileFormat.toUpperCase()}</Fmt>} · {((book.fileSize ?? 0) / 1e6).toFixed(1)} MB
              </span>
            </div>
          </div>
        </>
      )}

      <EditMetadataDialog open={editOpen} onOpenChange={setEditOpen} book={book} onSaved={refresh} />
      <ManualImportDialog open={importOpen} onOpenChange={setImportOpen}
        bookId={book.id} bookTitle={book.title} defaultLibraryId={book.libraryId}
        onImported={() => { refresh(); onChanged() }} />
      <ReleasesDialog book={searchOpen ? book : null} open={searchOpen} onOpenChange={setSearchOpen}
        onGrabbed={() => { toast("Watch Activity for progress"); refresh(); onChanged() }} />
    </section>
  )
}
