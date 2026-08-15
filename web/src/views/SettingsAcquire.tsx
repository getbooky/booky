import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { PathInput } from "@/components/PathInput"
import { api, acquisition } from "@/api"
import type { ApiProfile } from "@/api"
import { useApi } from "@/hooks/use-api"
import { toast } from "sonner"
import { ArrowDown, ArrowUp, X } from "lucide-react"
import { cn } from "@/lib/utils"

function Card({ title, desc, action, children }: { title: string; desc?: string; action?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="mb-5 rounded-lg border bg-surface p-6">
      <div className="mb-1 flex items-center justify-between gap-3">
        <h2 className="font-book text-xl font-bold">{title}</h2>
        {action}
      </div>
      {desc && <p className="mb-5 max-w-[62ch] text-[13px] text-muted-foreground">{desc}</p>}
      {children}
    </div>
  )
}

// SettingPills edits a newline-separated setting value as removable pills.
// Saves on every change; what an emptied list means is up to the caller's
// emptyNote. normalize cleans/validates a draft — return null to reject.
function SettingPills({ settingKey, label, placeholder, emptyNote, help, normalize, display }: {
  settingKey: string
  label?: string
  placeholder: string
  emptyNote: string
  help: string
  normalize: (draft: string) => string | null
  display?: (value: string) => string
}) {
  const [items, setItems] = useState<string[]>([])
  const [draft, setDraft] = useState("")
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    api.getSetting(settingKey).then(r => {
      setItems(r.value.split("\n").map(m => m.trim()).filter(Boolean))
      setLoaded(true)
    }).catch(() => setLoaded(true))
  }, [settingKey])

  const save = async (next: string[]) => {
    setItems(next)
    try {
      await api.putSetting(settingKey, next.join("\n"))
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  const add = () => {
    const m = normalize(draft)
    if (m === null) return
    if (!m || items.some(x => x.toLowerCase() === m.toLowerCase())) { setDraft(""); return }
    setDraft("")
    save([...items, m])
  }

  return (
    <div>
      {label && <Label>{label}</Label>}
      <div className="flex max-w-[520px] flex-wrap items-center gap-1.5">
        {items.map((m, i) => (
          <button key={m} type="button" onClick={() => save(items.filter((_, j) => j !== i))}
            className="group flex items-center gap-1.5 rounded-lg border border-brass/40 bg-brass/10 px-2.5 py-1 font-label text-[11.5px] text-brass hover:border-brass"
            title={`Remove ${m}`}>
            {display ? display(m) : m}
            <span className="opacity-60 group-hover:opacity-100">×</span>
          </button>
        ))}
        {loaded && items.length === 0 && (
          <span className="text-xs text-want">{emptyNote}</span>
        )}
      </div>
      <div className="mt-2 flex items-center gap-2">
        <Input value={draft} onChange={e => setDraft(e.target.value)}
          onKeyDown={e => { if (e.key === "Enter") add() }}
          placeholder={placeholder} className="h-8 max-w-[280px] font-label text-[11.5px]" />
        <Button variant="outline" className="h-8" disabled={!draft.trim()} onClick={add}>Add</Button>
      </div>
      <p className="mt-1.5 text-xs text-faint">{help}</p>
    </div>
  )
}

// MirrorPills: a direct source's mirror list — stock entries ship in the
// list and can be removed like anything else.
function MirrorPills({ settingKey, label }: { settingKey: string; label: string }) {
  return (
    <SettingPills settingKey={settingKey} label={label} placeholder="https://…"
      emptyNote="No mirrors — this source can't be searched until one is added."
      help="Tried top to bottom; changes save immediately. These sites rotate domains — add the current one here if yours stops working."
      display={m => m.replace(/^https?:\/\//, "")}
      normalize={draft => {
        const m = draft.trim().replace(/\/+$/, "")
        if (!m) return ""
        if (!/^https?:\/\//.test(m)) { toast.error("Mirrors start with http:// or https://"); return null }
        return m
      }} />
  )
}

// SourceToggle is a provider's on/off switch — saved immediately, so a
// disabled source drops out of every search without a separate Save click.
function SourceToggle({ settingKey, label }: { settingKey: string; label: string }) {
  const [value, setValue, loaded] = useSetting(settingKey)
  const on = value !== "false"
  const flip = async (v: boolean) => {
    setValue(v ? "true" : "false")
    try {
      await api.putSetting(settingKey, v ? "true" : "false")
      toast.success(v ? `${label} enabled` : `${label} disabled — it won't be searched`)
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  return (
    <span className="flex items-center gap-2">
      <span className="mono-label text-muted-foreground">{on ? "Enabled" : "Disabled"}</span>
      <Switch checked={loaded ? on : true} onCheckedChange={flip} aria-label={`Enable ${label}`} />
    </span>
  )
}

function Label({ children }: { children: React.ReactNode }) {
  return <div className="mono-label mb-1.5 text-muted-foreground">{children}</div>
}

// useSetting loads a settings key once and tracks edits.
function useSetting(key: string): [string, (v: string) => void, boolean] {
  const [value, setValue] = useState("")
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    api.getSetting(key).then(r => { setValue(r.value); setLoaded(true) }).catch(() => setLoaded(true))
  }, [key])
  return [value, setValue, loaded]
}

async function saveSettings(pairs: [string, string][], what: string) {
  try {
    for (const [k, v] of pairs) await api.putSetting(k, v)
    toast.success(`${what} saved`)
  } catch (e) {
    toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
  }
}

/* ---------- Sources panel ---------- */

const SOURCE_LABELS: Record<string, string> = {
  prowlarr: "Prowlarr indexers", annas: "Anna's Archive", zlibrary: "Z-Library",
}

export function SourcesPanel() {
  const [prowlarrUrl, setProwlarrUrl] = useSetting("prowlarr_url")
  const [prowlarrKey, setProwlarrKey] = useSetting("prowlarr_api_key")
  const [order, setOrder] = useSetting("source_order")
  const [annasKey, setAnnasKey] = useSetting("annas_key")
  const [zlibEmail, setZlibEmail] = useSetting("zlib_email")
  const [zlibPassword, setZlibPassword] = useSetting("zlib_password")
  const [testing, setTesting] = useState(false)
  const [testingZlib, setTestingZlib] = useState(false)

  const sources = (order || "prowlarr,annas,zlibrary").split(",").map(s => s.trim()).filter(Boolean)
  const move = (i: number, dir: -1 | 1) => {
    const next = [...sources]
    const j = i + dir
    if (j < 0 || j >= next.length) return
    ;[next[i], next[j]] = [next[j], next[i]]
    setOrder(next.join(","))
  }

  const test = async () => {
    setTesting(true)
    try {
      const r = await acquisition.testProwlarr(prowlarrUrl, prowlarrKey)
      toast.success(`Connected — Prowlarr ${r.version}, ${r.indexers} indexers`)
    } catch (e) {
      toast.error(`Connection failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setTesting(false)
    }
  }

  const testZlib = async () => {
    setTestingZlib(true)
    try {
      await saveSettings([["zlib_email", zlibEmail], ["zlib_password", zlibPassword]], "Z-Library")
      const r = await acquisition.testZlib()
      toast.success(`Connected — ${r.downloadsLeft} of ${r.downloadsLimit} downloads left today`)
    } catch (e) {
      toast.error(`Connection failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setTestingZlib(false)
    }
  }

  return (
    // providers stack on the left; the priority list rides shotgun on the
    // right so reordering is always in view while editing sources
    <div className="lg:grid lg:grid-cols-[minmax(0,1fr)_320px] lg:items-start lg:gap-5">
      <div>
      <Card title="Prowlarr"
        action={<SourceToggle settingKey="prowlarr_enabled" label="Prowlarr" />}
        desc="Booky never talks to indexers directly — Prowlarr owns them. Point at your instance and every enabled book indexer joins the search.">
        <div className="flex flex-col gap-4">
          <div>
            <Label>URL</Label>
            <Input value={prowlarrUrl} onChange={e => setProwlarrUrl(e.target.value)}
              placeholder="http://prowlarr:9696" className="max-w-[330px] text-[12.5px]" />
          </div>
          <div>
            <Label>API key</Label>
            <Input type="password" value={prowlarrKey} onChange={e => setProwlarrKey(e.target.value)}
              placeholder="from Prowlarr → Settings → General" className="max-w-[330px] text-[12.5px]" />
          </div>
          <div className="flex gap-2">
            <Button className="h-9" onClick={() => saveSettings([["prowlarr_url", prowlarrUrl], ["prowlarr_api_key", prowlarrKey]], "Prowlarr")}>Save</Button>
            <Button variant="outline" className="h-9" disabled={testing || !prowlarrUrl} onClick={test}>
              {testing ? "Testing…" : "Test connection"}
            </Button>
          </div>
        </div>
      </Card>

      <Card title="Z-Library"
        action={<SourceToggle settingKey="zlib_enabled" label="Z-Library" />}
        desc="Signs in with your own Z-Library account and downloads through its API — the same way the KoReader plugin does. A free account works, within its daily download limit. No account? Turn it off.">
        <div className="flex flex-col gap-4">
          <div>
            <Label>Email</Label>
            <Input value={zlibEmail} onChange={e => setZlibEmail(e.target.value)}
              placeholder="you@example.com" className="max-w-[330px] text-[12.5px]" />
          </div>
          <div>
            <Label>Password</Label>
            <Input type="password" value={zlibPassword} onChange={e => setZlibPassword(e.target.value)}
              placeholder="your Z-Library password" className="max-w-[330px] text-[12.5px]" />
            <p className="mt-1.5 text-xs text-faint">Stored server-side and never shown again.</p>
          </div>
          <MirrorPills settingKey="zlib_domains" label="Mirrors" />
          <div className="flex gap-2">
            <Button className="h-9" onClick={() => saveSettings([["zlib_email", zlibEmail], ["zlib_password", zlibPassword]], "Z-Library")}>Save</Button>
            <Button variant="outline" className="h-9" disabled={testingZlib || !zlibEmail} onClick={testZlib}>
              {testingZlib ? "Testing…" : "Test connection"}
            </Button>
          </div>
        </div>
      </Card>

      <Card title="Anna's Archive"
        action={<SourceToggle settingKey="annas_enabled" label="Anna's Archive" />}
        desc="A member key (from a paid membership) gives guaranteed fast downloads through Anna's API. Without an active membership, Booky uses the free slow-download servers — usually fine, but occasionally behind a verification wall it can't clear. If the free path keeps failing for you, turn the source off here.">
        <div className="flex flex-col gap-4">
          <div>
            <Label>Member secret key</Label>
            <Input type="password" value={annasKey} onChange={e => setAnnasKey(e.target.value)}
              placeholder="optional — from annas-archive.org account page" className="max-w-[330px] text-[12.5px]" />
            <p className="mt-1.5 text-xs text-faint">Optional, and always safe to paste in: it's used for fast downloads when your membership is active, and falls back to the free path when it isn't (non-member key or daily limit reached).</p>
          </div>
          <MirrorPills settingKey="annas_mirrors" label="Mirrors" />
          <Button className="h-9 w-fit" onClick={() => saveSettings([["annas_key", annasKey]], "Anna's Archive")}>Save</Button>
        </div>
      </Card>
      </div>

      <div className="lg:sticky lg:top-24">
        <Card title="Source Priority"
          desc="When the same book is available from several sources, this order breaks the tie — format and quality always come first.">
          <div className="flex flex-col">
            {sources.map((s, i) => (
              <div key={s} className="flex items-center gap-3 border-b border-linesoft py-2.5 last:border-b-0">
                <span className="font-label w-4 text-right text-xs text-faint">{i + 1}</span>
                <div className="flex flex-col">
                  <button aria-label={`Move ${s} up`} onClick={() => move(i, -1)} disabled={i === 0}
                    className={cn("text-faint hover:text-foreground", i === 0 && "opacity-30")}><ArrowUp className="h-3.5 w-3.5" /></button>
                  <button aria-label={`Move ${s} down`} onClick={() => move(i, 1)} disabled={i === sources.length - 1}
                    className={cn("text-faint hover:text-foreground", i === sources.length - 1 && "opacity-30")}><ArrowDown className="h-3.5 w-3.5" /></button>
                </div>
                <span className="text-[13.5px] font-medium">{SOURCE_LABELS[s] ?? s}</span>
              </div>
            ))}
          </div>
          <Button className="mt-4 h-9" onClick={() => saveSettings([["source_order", sources.join(",")]], "Source priority")}>Save</Button>
        </Card>
      </div>
    </div>
  )
}

/* ---------- Download clients panel ---------- */

export function ClientsPanel() {
  const [url, setUrl] = useSetting("sab_url")
  const [key, setKey] = useSetting("sab_api_key")
  const [category, setCategory, catLoaded] = useSetting("sab_category")
  const [downloads, setDownloads] = useSetting("downloads_dir")
  const [testing, setTesting] = useState(false)

  const test = async () => {
    setTesting(true)
    try {
      const r = await acquisition.testSab(url, key, category || "booky")
      toast.success(`Connected — SABnzbd ${r.version}`)
    } catch (e) {
      toast.error(`Connection failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setTesting(false)
    }
  }

  return (
    <>
      <Card title="SABnzbd"
        desc="Usenet grabs are sent here. Finished downloads import automatically — named, metadata written, and hardlinked into every library that wants the book.">
        <div className="flex flex-col gap-4">
          <div>
            <Label>URL</Label>
            <Input value={url} onChange={e => setUrl(e.target.value)} placeholder="http://sabnzbd:8080" className="max-w-[330px] text-[12.5px]" />
          </div>
          <div>
            <Label>API key</Label>
            <Input type="password" value={key} onChange={e => setKey(e.target.value)} placeholder="from SABnzbd → Config → General" className="max-w-[330px] text-[12.5px]" />
          </div>
          <div>
            <Label>Category</Label>
            <Input value={catLoaded && category === "" ? "" : category} onChange={e => setCategory(e.target.value)} placeholder="booky" className="max-w-[200px] text-[12.5px]" />
            <p className="mt-1.5 text-xs text-faint">Downloads are filed under this SAB category. Leave blank for "booky".</p>
          </div>
          <div className="flex gap-2">
            <Button className="h-9" onClick={() => saveSettings([["sab_url", url], ["sab_api_key", key], ["sab_category", category]], "SABnzbd")}>Save</Button>
            <Button variant="outline" className="h-9" disabled={testing || !url} onClick={test}>
              {testing ? "Testing…" : "Test connection"}
            </Button>
          </div>
        </div>
      </Card>

      <Card title="Direct Downloads"
        desc="Files from Anna's Archive and Libgen land here before import.">
        <Label>Downloads folder</Label>
        <div className="flex items-center gap-2">
          <div className="w-[330px]"><PathInput value={downloads} onChange={setDownloads} placeholder="/data/downloads/booky (default)" /></div>
          <Button variant="outline" className="h-9" onClick={() => saveSettings([["downloads_dir", downloads]], "Downloads folder")}>Save</Button>
        </div>
        <p className="mt-2 text-xs text-faint">Must be under /data. Changing it only affects new downloads — anything already downloaded is imported from where it landed.</p>
      </Card>
    </>
  )
}

/* ---------- Quality profiles panel ---------- */

const ALL_FORMATS = ["epub", "azw3", "mobi", "pdf", "cbz"]

export function ProfilesPanel() {
  const { data, loading, reload } = useApi(() => acquisition.profiles())
  const profile = data?.profiles?.[0]
  if (profile) return <ProfileEditor key={profile.id} profile={profile} onSaved={reload} />
  return (
    <Card title="Quality Profile">
      <p className="text-[13px] text-muted-foreground">
        {loading ? "loading…" : "No profile found — restart Booky and the default \"EPUB preferred\" profile is created automatically."}
      </p>
    </Card>
  )
}

// ProfilePills: pill editor for a profile-local list — unlike SettingPills
// nothing saves on change; the values ride along with "Save profile".
function ProfilePills({ items, onChange, placeholder, emptyNote, help }: {
  items: string[]
  onChange: (next: string[]) => void
  placeholder: string
  emptyNote?: string
  help: string
}) {
  const [draft, setDraft] = useState("")
  const add = () => {
    const v = draft.trim().toLowerCase()
    setDraft("")
    if (!v || items.includes(v)) return
    onChange([...items, v])
  }
  return (
    <div>
      <div className="flex max-w-[520px] flex-wrap items-center gap-1.5">
        {items.map((v, i) => (
          <button key={v} type="button" onClick={() => onChange(items.filter((_, j) => j !== i))}
            className="group flex items-center gap-1.5 rounded-lg border border-brass/40 bg-brass/10 px-2.5 py-1 font-label text-[11.5px] text-brass hover:border-brass"
            title={`Remove ${v}`}>
            {v}
            <span className="opacity-60 group-hover:opacity-100">×</span>
          </button>
        ))}
        {items.length === 0 && emptyNote && (
          <span className="text-xs text-want">{emptyNote}</span>
        )}
      </div>
      <div className="mt-2 flex items-center gap-2">
        <Input value={draft} onChange={e => setDraft(e.target.value)}
          onKeyDown={e => { if (e.key === "Enter") add() }}
          placeholder={placeholder} className="h-8 max-w-[280px] font-label text-[11.5px]" />
        <Button variant="outline" className="h-8" disabled={!draft.trim()} onClick={add}>Add</Button>
      </div>
      <p className="mt-1.5 text-xs text-faint">{help}</p>
    </div>
  )
}

const splitTerms = (s: string) => s.split(/[\n,]/).map(t => t.trim()).filter(Boolean)

function ProfileEditor({ profile, onSaved }: { profile: ApiProfile; onSaved: () => void }) {
  const [name, setName] = useState(profile.name)
  const [formats, setFormats] = useState<string[]>(profile.formats)
  const [cutoff, setCutoff] = useState(profile.cutoffFormat)
  const [languages, setLanguages] = useState<string[]>(splitTerms(profile.languages ?? ""))
  const [preferred, setPreferred] = useState<string[]>(splitTerms(profile.preferredTerms))
  const [avoided, setAvoided] = useState<string[]>(splitTerms(profile.avoidedTerms))

  const move = (i: number, dir: -1 | 1) => {
    const next = [...formats]
    const j = i + dir
    if (j < 0 || j >= next.length) return
    ;[next[i], next[j]] = [next[j], next[i]]
    setFormats(next)
  }
  const unused = ALL_FORMATS.filter(f => !formats.includes(f))

  const save = async () => {
    try {
      await acquisition.updateProfile(profile.id, {
        name, formats, cutoffFormat: formats.includes(cutoff) ? cutoff : formats[0],
        languages: languages.join("\n"),
        preferredTerms: preferred.join(", "), avoidedTerms: avoided.join(", "),
      })
      toast.success("Profile saved")
      onSaved()
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <Card title="Quality Profile"
      desc="Format order decides everything: a worse-term copy in a better format always wins. The cutoff is where upgrading stops.">
      <div className="flex flex-col gap-5">
        <div>
          <Label>Name</Label>
          <Input value={name} onChange={e => setName(e.target.value)} className="max-w-[280px] text-[12.5px]" />
        </div>
        <div>
          <Label>Formats, best first</Label>
          <div className="flex max-w-[380px] flex-col">
            {formats.map((f, i) => (
              <div key={f} className="flex items-center gap-3 border-b border-linesoft py-2 last:border-b-0">
                <span className="font-label w-4 text-right text-xs text-faint">{i + 1}</span>
                <div className="flex flex-col">
                  <button aria-label={`Move ${f} up`} onClick={() => move(i, -1)} disabled={i === 0}
                    className={cn("text-faint hover:text-foreground", i === 0 && "opacity-30")}><ArrowUp className="h-3.5 w-3.5" /></button>
                  <button aria-label={`Move ${f} down`} onClick={() => move(i, 1)} disabled={i === formats.length - 1}
                    className={cn("text-faint hover:text-foreground", i === formats.length - 1 && "opacity-30")}><ArrowDown className="h-3.5 w-3.5" /></button>
                </div>
                <span className="flex-1 text-[13.5px] font-medium uppercase">{f}</span>
                {f === cutoff && <span className="font-label rounded-md border border-brass/50 px-1.5 py-px text-[9px] uppercase text-brass">cutoff</span>}
                <button aria-label={`Make ${f} the cutoff`} className="text-[11.5px] text-faint hover:text-brass" onClick={() => setCutoff(f)}>set cutoff</button>
                <button aria-label={`Remove ${f}`} disabled={formats.length === 1} onClick={() => setFormats(formats.filter(x => x !== f))}
                  className={cn("text-faint hover:text-want", formats.length === 1 && "opacity-30")}><X className="h-3.5 w-3.5" /></button>
              </div>
            ))}
          </div>
          {unused.length > 0 && (
            <div className="mt-2 flex flex-wrap gap-1.5">
              {unused.map(f => (
                <Button key={f} variant="outline" className="h-7 px-2 text-[11.5px] uppercase"
                  onClick={() => setFormats([...formats, f])}>+ {f}</Button>
              ))}
            </div>
          )}
        </div>
        <div>
          <Label>Languages</Label>
          <ProfilePills items={languages} onChange={setLanguages} placeholder="e.g. english"
            emptyNote="No languages — the filter is off, any language can be grabbed."
            help="Releases in any other language are skipped on every source — a release that doesn't say its language is assumed fine." />
        </div>
        <div className="grid gap-4 md:grid-cols-2">
          <div>
            <Label>Preferred terms</Label>
            <ProfilePills items={preferred} onChange={setPreferred} placeholder="e.g. retail"
              help="Releases containing these rank higher within a format." />
          </div>
          <div>
            <Label>Avoided terms</Label>
            <ProfilePills items={avoided} onChange={setAvoided} placeholder="e.g. scan"
              help="Releases containing these rank lower, but still beat a worse format." />
          </div>
        </div>
        <Button className="h-9 w-fit" onClick={save}>Save profile</Button>
      </div>
    </Card>
  )
}
