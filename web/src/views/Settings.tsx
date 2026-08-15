import { useEffect, useState } from "react"
import { Folio, Tag, SectionLabel } from "@/components/bits"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { toast } from "sonner"
import { Plus, RefreshCw, Trash2 } from "lucide-react"
import { api, acquisition } from "@/api"
import type { ApiLibrary, ApiProfile, ReviewFile, SearchResult } from "@/api"
import { PathInput } from "@/components/PathInput"
import { useApi } from "@/hooks/use-api"
import { SetupWizard } from "@/components/SetupWizard"
import { DeleteLibraryDialog } from "@/components/DeleteLibraryDialog"
import { SourcesPanel, ClientsPanel, ProfilesPanel } from "@/views/SettingsAcquire"
import { ListsPanel } from "@/views/SettingsLists"
import { UsersPanel, BackupsPanel, DevicesPanel } from "@/views/SettingsDelivery"
import { delivery } from "@/api"
import { cn } from "@/lib/utils"
import { useAccess } from "@/lib/access"

type Panel = "libraries" | "profiles" | "metadata" | "sources" | "clients" | "lists" | "koreader" | "backups" | "users" | "about" | "health" | "logs"

const NAV: { id: Panel; label: string }[] = [
  { id: "libraries", label: "Libraries" },
  { id: "profiles", label: "Quality Profiles" },
  { id: "metadata", label: "Metadata" },
  { id: "sources", label: "Sources" },
  { id: "clients", label: "Download Clients" },
  { id: "lists", label: "Watched Lists" },
  { id: "koreader", label: "KoReader Devices" },
  { id: "backups", label: "Backups" },
  { id: "users", label: "Users" },
  { id: "health", label: "Health" },
  { id: "logs", label: "Logs" },
  { id: "about", label: "About" },
]

function Card({ title, desc, action, children }: { title: string; desc?: string; action?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="mb-5 rounded-lg border bg-surface p-6">
      <div className="mb-1 flex items-center gap-2.5">
        <h2 className="font-book text-xl font-bold">{title}</h2>
        {action}
      </div>
      {desc && <p className="mb-5 max-w-[62ch] text-[13px] text-muted-foreground">{desc}</p>}
      {children}
    </div>
  )
}

function FieldLabel({ children }: { children: React.ReactNode }) {
  return <div className="mono-label mb-1.5 text-muted-foreground">{children}</div>
}

/* ---------- file-naming card (live preview against real roots) ---------- */

// sanitizeComponent mirrors the Go importer's sanitize(): replace path-hazard
// characters and neutralize empty / all-dots components, so the preview shows
// exactly what lands on disk.
function sanitizeComponent(part: string): string {
  const s = part
    .replace(/[/\\|]/g, "-")
    .replace(/:/g, " -")
    .replace(/[*?<>]/g, "")
    .replace(/"/g, "'")
    .trim()
  return s === "" || /^\.+$/.test(s) ? "_" : s
}

// renderScheme expands a naming scheme against a sample book — the relative
// path under a library root, extension appended. Each token value is sanitized
// before substitution, exactly like the importer, so a slash inside a value
// can't create a folder; only the scheme's own slashes split directories.
// The sample values are deliberately placeholders rather than a real book:
// the preview is there to show where each token lands in the path, and a real
// author's name reads like Booky is recommending them.
function renderScheme(scheme: string, title = "Book Title"): string {
  const sample: Record<string, string> = {
    Author: "Author Name", Title: title, Series: "Series Name",
    SeriesNum: "1", Year: "2024",
  }
  const rendered = (scheme.trim() || "{Author}/{Title}")
    .replace(/\{(\w+)\}/g, (m, token) => (token in sample ? sanitizeComponent(sample[token]) : m))
  return `${rendered}.epub`
}

function NamingCard({ libraries }: { libraries: ApiLibrary[] }) {
  const [scheme, setScheme] = useState("")
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    api.getSetting("naming_scheme").then(r => { setScheme(r.value); setLoaded(true) }).catch(() => setLoaded(true))
  }, [])
  const rel = renderScheme(scheme)

  return (
    <Card title="File Naming" desc="One scheme for every library. It's the path built under each library's own root folder — the extension is always added for you, so there's no {Format} token.">
      <FieldLabel>Naming scheme</FieldLabel>
      <div className="flex items-center gap-2">
        <Input value={scheme} disabled={!loaded} onChange={e => setScheme(e.target.value)}
          placeholder="{Author}/{Title}" className="max-w-[330px] text-[12.5px]" />
        <Button variant="outline" className="h-9" onClick={async () => {
          try { await api.putSetting("naming_scheme", scheme); toast.success("Naming scheme saved") }
          catch (e) { toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`) }
        }}>Save</Button>
      </div>
      <p className="mt-1.5 text-xs text-faint">Tokens: {"{Author} {Title} {Series} {SeriesNum} {Year}"} — slashes make folders.</p>

      <div className="mt-4 border-t pt-3">
        <div className="mono-label mb-2 text-faint">On disk</div>
        {libraries.length === 0 && (
          <p className="text-[12.5px] text-muted-foreground">
            <span className="text-faint">{"<library root>"}</span>/{rel}
          </p>
        )}
        <div className="flex flex-col gap-1">
          {libraries.map(l => (
            <p key={l.id} className="text-[12.5px] text-muted-foreground">
              <span className="text-faint">{l.rootPath.replace(/\/$/, "")}/</span>{rel}
            </p>
          ))}
        </div>
      </div>

    </Card>
  )
}

export function SettingField({ settingKey, label, hint, placeholder, className, secret, preview }: {
  settingKey: string
  label: string
  hint?: string
  placeholder?: string
  className?: string
  secret?: boolean
  preview?: (value: string) => string
}) {
  const [value, setValue] = useState("")
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    api.getSetting(settingKey).then(r => { setValue(r.value); setLoaded(true) }).catch(() => setLoaded(true))
  }, [settingKey])

  const save = async () => {
    try {
      await api.putSetting(settingKey, value)
      toast.success(`${label} saved`)
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  return (
    <div>
      <FieldLabel>{label}</FieldLabel>
      <div className="flex items-center gap-2">
        <Input type={secret ? "password" : "text"} value={value} placeholder={placeholder}
          onChange={e => setValue(e.target.value)} disabled={!loaded}
          className={cn("max-w-[330px] text-[12.5px]", className)} />
        <Button variant="outline" className="h-9" onClick={save}>Save</Button>
      </div>
      {preview && value !== "" && (
        <p className="mt-2 text-[12px] text-muted-foreground">
          <span className="mono-label mr-1.5 text-faint">preview</span>{preview(value)}
        </p>
      )}
      {hint && <p className="mt-1.5 max-w-[58ch] text-xs text-faint">{hint}</p>}
    </div>
  )
}

/* ---------- Libraries panel (live) ---------- */

export function ReviewScreen({ library, onBack }: { library: ApiLibrary; onBack: () => void }) {
  const { data, reload } = useApi(() => api.reviewQueue(library.id))
  const files = data?.files ?? []
  const [candidates, setCandidates] = useState<Record<number, SearchResult[]>>({})

  const findMatches = async (f: ReviewFile) => {
    try {
      const q = [f.guessTitle, f.guessAuthor].filter(Boolean).join(" ")
      const res = await api.search(q)
      setCandidates(c => ({ ...c, [f.id]: (res.results ?? []).slice(0, 3) }))
    } catch (e) {
      toast.error(`Search failed: ${e instanceof Error ? e.message : e}`)
    }
  }
  const accept = async (f: ReviewFile, meta: SearchResult) => {
    try {
      const book = await api.reviewMatch(f.id, meta)
      toast.success(`Matched: ${book.title}`)
      reload()
    } catch (e) {
      toast.error(`Couldn't match: ${e instanceof Error ? e.message : e}`)
    }
  }
  const ignore = async (f: ReviewFile) => {
    await api.reviewIgnore(f.id)
    toast(`${f.path.split("/").pop()} ignored`)
    reload()
  }

  return (
    <div className="mb-5 rounded-lg border bg-surface p-6">
      <button onClick={onBack} className="mono-label mb-4 flex items-center gap-1.5 text-muted-foreground hover:text-brass">
        ← Libraries
      </button>
      <h2 className="font-book mb-1 text-xl font-bold">Import failed — {library.name}</h2>
      <p className="mb-5 max-w-[62ch] text-[13px] text-muted-foreground">
        Files are matched in place, never moved. Search for the right book, or ignore files that aren't books.
      </p>
      {files.length === 0 && <p className="text-[13px] text-muted-foreground">No failed imports — everything matched.</p>}
      <div className="border-t">
        {files.map(f => (
          <div key={f.id} className="border-b border-linesoft px-1 py-3.5">
            <div className="truncate text-[12px] text-muted-foreground">{f.path}</div>
            <div className="mt-2 flex flex-wrap items-center gap-3">
              <span className="text-[13px]">
                Guess: <b>{f.guessTitle || "?"}</b>{f.guessAuthor ? ` — ${f.guessAuthor}` : ""}
              </span>
              <div className="ml-auto flex gap-2">
                <Button variant="outline" className="h-8" onClick={() => findMatches(f)}>Find matches</Button>
                <Button variant="outline" className="h-8 text-faint" onClick={() => ignore(f)}>Ignore</Button>
              </div>
            </div>
            {(candidates[f.id] ?? []).map((c, i) => (
              <div key={i} className="mt-2 flex items-center gap-3 border-l-2 border-brass/40 py-1 pl-3 text-[13px]">
                <span className="min-w-0 flex-1 truncate">
                  <b>{c.title}</b> — {(c.authors ?? []).join(", ")}
                  {c.seriesName ? ` · ${c.seriesName}${c.seriesIndex ? ` #${c.seriesIndex}` : ""}` : ""}
                </span>
                <Button className="h-7" onClick={() => accept(f, c)}>Use</Button>
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  )
}

// OPDSCreds: per-library feed credentials — set them to switch the feed on.
function OPDSCreds({ library, onSaved }: { library: ApiLibrary; onSaved: () => void }) {
  const [open, setOpen] = useState(false)
  const [username, setUsername] = useState(library.opdsUsername)
  const [password, setPassword] = useState("")

  const save = async () => {
    try {
      const r = await delivery.setOPDS(library.id, username.trim(), password)
      toast.success(`OPDS feed live at ${r.feedUrl}`)
      setOpen(false); setPassword("")
      onSaved()
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }

  if (!open) {
    return (
      <button className="mono-label text-muted-foreground hover:text-brass" onClick={() => setOpen(true)}>
        {library.opdsConfigured
          ? <>opds: /opds/{library.id} · <span className="text-good">{library.opdsUsername} ✓</span> · change credentials</>
          : <>opds: /opds/{library.id} · set credentials</>}
      </button>
    )
  }
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Input value={username} onChange={e => setUsername(e.target.value)} placeholder="feed username" className="h-8 w-[130px] text-[12px]" />
      <Input type="password" value={password} onChange={e => setPassword(e.target.value)} placeholder="password (8+ chars)" className="h-8 w-[150px] text-[12px]" />
      <Button className="h-8" disabled={!username.trim() || password.length < 8} onClick={save}>Save</Button>
      <Button variant="outline" className="h-8" onClick={() => setOpen(false)}>Cancel</Button>
    </span>
  )
}

// ProfilePicker assigns a quality profile to one library.
function ProfilePicker({ library, profiles, onSaved }: { library: ApiLibrary; profiles: ApiProfile[]; onSaved: () => void }) {
  if (profiles.length <= 1) return null
  return (
    <select className="h-8 rounded-md border border-input bg-transparent px-2 text-[12px]"
      value={library.qualityProfileId}
      onChange={async e => {
        try {
          await api.setLibraryProfile(library.id, Number(e.target.value))
          toast.success(`${library.name}: profile updated`)
          onSaved()
        } catch (err) {
          toast.error(`Couldn't save: ${err instanceof Error ? err.message : err}`)
        }
      }}>
      {profiles.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
    </select>
  )
}

function LibrariesPanel() {
  const { data, loading, reload } = useApi(() => api.libraries())
  const libraries = data?.libraries ?? []
  const profilesQuery = useApi(() => acquisition.profiles())
  const profiles = profilesQuery.data?.profiles ?? []
  const [reviewLib, setReviewLib] = useState<ApiLibrary | null>(null)
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState("")
  const [rootPath, setRootPath] = useState("/data/books/")
  const [scanning, setScanning] = useState<number | null>(null)
  const [deleteLib, setDeleteLib] = useState<ApiLibrary | null>(null)

  const create = async () => {
    try {
      await api.createLibrary(name.trim(), rootPath.trim())
      toast.success(`Library "${name.trim()}" created`)
      setAdding(false); setName("")
      reload()
    } catch (e) {
      toast.error(`Couldn't create: ${e instanceof Error ? e.message : e}`)
    }
  }
  const scan = async (l: ApiLibrary) => {
    setScanning(l.id)
    try {
      const r = await api.scanLibrary(l.id)
      toast.success(`${l.name}: ${r.scanned} files · ${r.matched} matched · ${r.review} need review`)
      reload()
    } catch (e) {
      toast.error(`Scan failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setScanning(null)
    }
  }

  if (reviewLib) return <ReviewScreen library={reviewLib} onBack={() => { setReviewLib(null); reload() }} />

  return (
    <>
      <Card title="Libraries"
        action={
          <Button variant="outline" size="icon" className="h-7 w-7 rounded-full" aria-label="Add library" onClick={() => setAdding(a => !a)}>
            <Plus className="h-4 w-4" />
          </Button>
        }
        desc="Each library is a root folder with its own OPDS feed and quality profile. Watched lists route their books into one of these.">
        <div className="flex flex-col gap-2">
          {loading && <p className="mono-label text-faint">loading…</p>}
          {libraries.map(l => (
            <div key={l.id} className="flex flex-wrap items-center gap-3 rounded-lg border border-linesoft bg-surface2 px-4 py-3">
              <div className="min-w-[120px]">
                <div className="font-book text-[16px] font-bold">{l.name}</div>
                <div className="text-[11.5px] text-faint">{l.rootPath}</div>
                <div className="mt-1"><OPDSCreds library={l} onSaved={reload} /></div>
              </div>
              <span className="text-xs text-muted-foreground">{l.bookCount} books · {l.onShelf} on shelf</span>
              <ProfilePicker library={l} profiles={profiles} onSaved={reload} />
              <div className="ml-auto flex gap-2">
                <Button variant="outline" className="h-8" disabled={scanning === l.id} onClick={() => scan(l)}>
                  {scanning === l.id ? "Scanning…" : "Scan folder"}
                </Button>
                {l.reviewCount > 0 && (
                  <Button variant="outline" className="h-8 border-brass text-brass" onClick={() => setReviewLib(l)}>
                    Import failed
                    <span className="font-label ml-1.5 flex h-[16px] min-w-[16px] items-center justify-center rounded-full bg-brass px-1 text-[9.5px] font-bold text-brass-ink">
                      {l.reviewCount}
                    </span>
                  </Button>
                )}
                <Button variant="outline" size="icon" aria-label={`Delete ${l.name}`} title="Delete library"
                  className="h-8 w-8 text-muted-foreground hover:border-want hover:text-want"
                  onClick={() => setDeleteLib(l)}>
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          ))}
          {adding ? (
            <div className="flex flex-wrap items-end gap-2 rounded-lg border border-dashed border-linesoft px-4 py-3">
              <div>
                <FieldLabel>Name</FieldLabel>
                <Input value={name} onChange={e => setName(e.target.value)} className="h-9 max-w-[140px]" placeholder="Library name" />
              </div>
              <div className="min-w-[240px] flex-1">
                <FieldLabel>Root path (under /data)</FieldLabel>
                <PathInput value={rootPath} onChange={setRootPath} className="h-9" />
              </div>
              <Button className="h-9" disabled={!name.trim() || !rootPath.trim()} onClick={create}>Create</Button>
              <Button variant="outline" className="h-9" onClick={() => setAdding(false)}>Cancel</Button>
            </div>
          ) : null}
        </div>
      </Card>

      <NamingCard libraries={libraries} />

      <DeleteLibraryDialog library={deleteLib} onOpenChange={v => { if (!v) setDeleteLib(null) }}
        onDeleted={() => reload()} />
    </>
  )
}

/* ---------- Metadata panel (Hardcover token + options) ---------- */

function MetadataPanel() {
  return (
    <Card title="Metadata" desc="Hardcover drives all metadata — titles, series, descriptions, covers. Goodreads is only ever a list source: books found on your watched lists are matched against Hardcover and adopt its record. Add your Hardcover token below; without it, books keep whatever the list feed carried until a refresh finds them a match.">
      <div className="grid max-w-2xl gap-5">
        <div>
          <SettingField settingKey="hardcover_token" label="Hardcover API token" secret
            placeholder="paste token from hardcover.app settings"
            hint="Stored server-side and never shown again. Get one at hardcover.app → settings → API." />
          <Button variant="outline" className="mt-2 h-8" onClick={async () => {
            try {
              await acquisition.testHardcover()
              toast.success("Connected — Hardcover token works")
            } catch (e) {
              toast.error(`Token failed (save it first): ${e instanceof Error ? e.message : e}`)
            }
          }}>Test token</Button>
        </div>
      </div>

      <SectionLabel className="mt-6">Series overlay</SectionLabel>
      <SeriesOverlayToggle />

      <SectionLabel className="mt-6">Never import</SectionLabel>
      <ExcludePatterns />

      <SectionLabel className="mt-6">File writing</SectionLabel>
      <WriteToggles />
    </Card>
  )
}

// SeriesOverlayToggle: Goodreads series pages merged into watched series
// during the weekly bibliography sync — announced books due within a year
// and novellas Hardcover hasn't linked. Rows it adds are catalog-only and
// live only while the feature is on AND Goodreads still lists them; turning
// it off prunes them again on each author's next sync (books you've added
// to a library or edited always stay).
function SeriesOverlayToggle() {
  const [enabled, setEnabled] = useState(false)
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    api.getSetting("series_overlay").then(r => { setEnabled(r.value === "true"); setLoaded(true) }).catch(() => setLoaded(true))
  }, [])
  const save = async (v: boolean) => {
    setEnabled(v)
    try {
      await api.putSetting("series_overlay", v ? "true" : "false")
      toast.success(v
        ? "Series overlay on — fills in on each author's next sync"
        : "Series overlay off — its entries clear on each author's next sync")
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  if (!loaded) return <p className="mono-label text-faint">loading…</p>
  return (
    <div className="max-w-2xl">
      <label className="flex cursor-pointer items-start gap-2.5 text-[13px]">
        <input type="checkbox" className="mt-0.5 h-4 w-4 accent-[hsl(var(--brass))]" checked={enabled}
          onChange={e => void save(e.target.checked)} />
        <span>
          <span className="font-semibold">Fill series gaps from Goodreads</span>
          <span className="mt-0.5 block text-xs text-muted-foreground">
            Adds announced books due within a year and novellas Hardcover hasn't linked to your
            watched series, with release dates for the calendar. Entries stay catalog-only — never
            downloaded or monitored on their own. Turn it off and they clean themselves up on each
            author's next sync; anything you've shelved or edited stays.
          </span>
        </span>
      </label>
    </div>
  )
}

// ExcludePatterns: user-editable title filters, layered on the built-in
// box-set/omnibus heuristics. Applies everywhere books enter Booky: search
// results, watched lists, and bibliography imports.
function ExcludePatterns() {
  const [terms, setTerms] = useState<string[]>([])
  const [draft, setDraft] = useState("")
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    api.getSetting("exclude_patterns").then(r => {
      setTerms(r.value.split("\n").map(t => t.trim()).filter(Boolean))
      setLoaded(true)
    }).catch(() => setLoaded(true))
  }, [])

  const save = async (next: string[]) => {
    setTerms(next)
    try {
      await api.putSetting("exclude_patterns", next.join("\n"))
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  const add = () => {
    const t = draft.trim().toLowerCase()
    if (!t) return
    if (terms.some(x => x.toLowerCase() === t)) { setDraft(""); return }
    setDraft("")
    save([...terms, t])
  }

  return (
    <div className="max-w-2xl">
      <p className="mb-3 max-w-[58ch] text-[13px] text-muted-foreground">
        Titles containing any of these terms are never pulled in — not from search, watched lists, or
        bibliography imports. Matched as whole words, case-insensitive. The stock terms (box set,
        omnibus, …) are all removable — take one out if you actually want those books.
      </p>
      <div className="flex max-w-[560px] flex-wrap items-center gap-1.5">
        {terms.map((t, i) => (
          <button key={t} onClick={() => save(terms.filter((_, j) => j !== i))}
            className="group flex items-center gap-1.5 rounded-lg border border-brass/40 bg-brass/10 px-2.5 py-1 text-[12px] text-brass hover:border-brass"
            title={`Remove "${t}"`}>
            {t}
            <span className="opacity-60 group-hover:opacity-100">×</span>
          </button>
        ))}
        {loaded && terms.length === 0 && (
          <span className="text-xs text-faint">Nothing excluded — every title comes through.</span>
        )}
      </div>
      <div className="mt-3 flex items-center gap-2">
        <Input value={draft} onChange={e => setDraft(e.target.value)}
          onKeyDown={e => { if (e.key === "Enter") add() }}
          placeholder="add a term — large print, sneak peek…" className="h-9 max-w-[280px] text-[12.5px]" />
        <Button variant="outline" className="h-9" disabled={!draft.trim()} onClick={add}>Add</Button>
      </div>
      <p className="mt-2 text-xs text-faint">
        "Books 1–3"-style box-set titles are always filtered — that shape is never a real book.
      </p>
    </div>
  )
}

// The fields Booky can write into the EPUB itself on import. Keys match the
// metadata_write setting and the backend's epub.Fields.
const WRITE_FIELDS: { key: string; label: string }[] = [
  { key: "title", label: "Title" },
  { key: "author", label: "Author" },
  { key: "series", label: "Series" },
  { key: "description", label: "Description" },
  { key: "language", label: "Language" },
  { key: "publisher", label: "Publisher" },
  { key: "pubdate", label: "Publication date" },
  { key: "cover", label: "Cover image" },
  { key: "identifiers", label: "Identifiers" },
]

function WriteToggles() {
  const [enabled, setEnabled] = useState<Set<string>>(new Set())
  const [writeOnImport, setWriteOnImport] = useState(true)
  useEffect(() => {
    api.getSetting("metadata_write")
      .then(r => setEnabled(new Set(r.value.split(",").map(s => s.trim()).filter(Boolean))))
      .catch(() => setEnabled(new Set(WRITE_FIELDS.map(f => f.key))))
    api.getSetting("write_on_import").then(r => setWriteOnImport(r.value !== "false")).catch(() => {})
  }, [])

  const save = async (next: Set<string>, nextWrite: boolean) => {
    try {
      await api.putSetting("metadata_write", [...next].join(","))
      await api.putSetting("write_on_import", nextWrite ? "true" : "false")
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  const toggle = (key: string) => {
    const next = new Set(enabled)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setEnabled(next)
    void save(next, writeOnImport)
  }

  return (
    <div className="max-w-2xl">
      <p className="mb-3 max-w-[56ch] text-[13px] text-muted-foreground">
        On import, Booky writes the checked fields into the EPUB itself, so the file carries its
        metadata onto any device. Unchecked fields are left exactly as the file came.
      </p>
      <label className="mb-3 flex w-fit cursor-pointer items-center gap-2.5 text-[13.5px] font-medium">
        <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]" checked={writeOnImport}
          onChange={e => { setWriteOnImport(e.target.checked); void save(enabled, e.target.checked) }} />
        Write metadata into files on import
      </label>
      <div className={cn("grid gap-x-8 gap-y-1.5 border-t pt-3 sm:grid-cols-2", !writeOnImport && "pointer-events-none opacity-40")}>
        {WRITE_FIELDS.map(f => (
          <label key={f.key} className="flex cursor-pointer items-center gap-2.5 py-1 text-[13px]">
            <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]"
              checked={enabled.has(f.key)} onChange={() => toggle(f.key)} />
            <span className="font-medium">{f.label}</span>
          </label>
        ))}
      </div>
    </div>
  )
}

/* ---------- About ---------- */

function AboutPanel() {
  // About is one of the two panels a scoped user can open, so the wizard
  // button has to be gated here rather than at the panel. Every step of the
  // wizard — creating libraries, writing provider credentials, adding the
  // admin account — is admin-only server-side, so a user clicking through it
  // could never actually change anything; they'd just be walked through a
  // sequence of failures.
  const { isAdmin } = useAccess()
  const [wizardOpen, setWizardOpen] = useState(false)
  const { data } = useApi(() => api.status())
  return (
    <>
      {isAdmin && <SetupWizard open={wizardOpen} onOpenChange={setWizardOpen} />}
      <Card title="About" desc="Booky runs as a single Docker container. Updates arrive as image tags — pull manually or let watchtower handle it.">
        <div className="flex flex-wrap items-center gap-3">
          <span className="font-book text-[17px] font-bold">Booky {data?.version ?? "…"}</span>
          <Tag kind={data?.status === "ok" ? "good" : "want"}>{data?.status ?? "connecting"}</Tag>
        </div>
        <p className="mt-3 text-[11.5px] text-faint">image: ghcr.io/getbooky/booky:latest · PUID 99 · PGID 100 · UMASK 022</p>
        {isAdmin && (
          <Button variant="outline" className="mt-4 h-9" onClick={() => setWizardOpen(true)}>
            Run setup wizard
          </Button>
        )}
      </Card>
    </>
  )
}

/* ---------- Health & Logs panels (live) ---------- */

function HealthPanel() {
  const { data, loading, reload } = useApi(() => api.health())
  return (
    <Card title="Health"
      desc="Live checks on the pieces Booky depends on. Green means working right now."
      action={
        <Button variant="outline" className="h-8" onClick={() => reload()}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" /> Re-check
        </Button>
      }>
      {loading && <p className="text-[13px] text-muted-foreground">checking…</p>}
      <div className="flex flex-col">
        {(data?.checks ?? []).map(c => (
          <div key={c.name} className="flex items-center gap-3 border-b border-linesoft py-3 last:border-b-0">
            <span className={cn("h-2.5 w-2.5 shrink-0 rounded-full",
              c.status === "ok" ? "bg-good" : c.status === "error" ? "bg-want" : "border border-faint")} />
            <span className={cn("min-w-[200px] text-[13.5px] font-medium", c.status === "pending" && "text-muted-foreground")}>{c.name}</span>
            <span className="truncate text-[12.5px] text-muted-foreground">{c.detail}</span>
          </div>
        ))}
      </div>
    </Card>
  )
}

function LogsPanel() {
  const { data, loading, reload } = useApi(() => api.logs())
  const lines = data?.lines ?? []
  return (
    <Card title="Logs"
      desc="The most recent activity, newest last. The full log streams to the container console."
      action={
        <Button variant="outline" className="h-8" onClick={() => reload()}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" /> Refresh
        </Button>
      }>
      {loading && <p className="text-[13px] text-muted-foreground">loading…</p>}
      {!loading && lines.length === 0 && <p className="text-[13px] text-muted-foreground">Nothing logged yet.</p>}
      {lines.length > 0 && (
        <pre className="max-h-[420px] overflow-auto rounded-lg bg-surface2 p-4 font-label text-[11px] leading-[1.7] text-muted-foreground">
          {lines.join("\n")}
        </pre>
      )}
    </Card>
  )
}

/* ---------- Settings view ---------- */

// USER_PANELS is everything a non-admin account may open. Pairing an e-reader
// is everyday business, not administration, so the KoReader page stays — the
// library picker inside it is already limited to the libraries that account
// holds, and the devices listed are only their own. Everything else on this
// screen configures the install and would answer 403.
//
// Anything ADDED to a panel listed here needs its own check: the panel being
// user-visible does not make its contents so. The About panel's "Run setup
// wizard" button was the one that got through — harmless in the end, since
// every step of the wizard is admin-only server-side, but it offered a user a
// flow that could only fail.
const USER_PANELS: Panel[] = ["koreader", "about"]

export function SettingsView() {
  const { isAdmin } = useAccess()
  const nav = isAdmin ? NAV : NAV.filter(n => USER_PANELS.includes(n.id))
  const [panel, setPanel] = useState<Panel>(() => (isAdmin ? "libraries" : "koreader"))
  return (
    <section>
      <Folio title="Settings" end={isAdmin ? (
        <Button variant="outline" className="h-9" onClick={async () => {
          if (!window.confirm("Restart Booky now?")) return
          try {
            await delivery.restart()
            toast("Restarting — back in a few seconds")
          } catch (e) {
            toast.error(`Restart failed: ${e instanceof Error ? e.message : e}`)
          }
        }}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" /> Restart
        </Button>
      ) : undefined} />
      <div className="flex flex-col items-start gap-7">
        <nav className="flex w-full flex-row flex-wrap gap-x-1 border-b">
          {nav.map(n => (
            <button
              key={n.id}
              onClick={() => setPanel(n.id)}
              className={cn(
                "-mb-px flex items-center gap-1.5 border-b-2 border-transparent px-3 py-2.5 text-[13px] font-medium text-muted-foreground hover:text-foreground",
                panel === n.id && "border-brass text-brass"
              )}
            >
              {n.label}
            </button>
          ))}
        </nav>
        <div className="w-full max-w-[880px]">
          {panel === "libraries" && <LibrariesPanel />}
          {panel === "metadata" && <MetadataPanel />}
          {panel === "health" && <HealthPanel />}
          {panel === "logs" && <LogsPanel />}
          {panel === "about" && <AboutPanel />}
          {panel === "profiles" && <ProfilesPanel />}
          {panel === "sources" && <SourcesPanel />}
          {panel === "clients" && <ClientsPanel />}
          {panel === "lists" && <ListsPanel />}
          {panel === "koreader" && <DevicesPanel />}
          {panel === "backups" && <BackupsPanel />}
          {panel === "users" && <UsersPanel />}
        </div>
      </div>
    </section>
  )
}
