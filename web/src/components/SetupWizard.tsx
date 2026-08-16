import { useEffect, useState } from "react"
import { Dialog, DialogContent } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { api, acquisition, delivery, kindle, watchers } from "@/api"
import { PathInput } from "@/components/PathInput"

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mono-label mb-1.5 text-muted-foreground">{label}</div>
      {children}
    </div>
  )
}

const STEPS = ["Welcome", "Your admin account", "Libraries", "Metadata", "Quality profile", "Indexers", "SABnzbd", "Direct downloads", "Watched lists", "KoReader", "Send to Kindle"] as const

// The admin account comes first and is the one mandatory step: creating it
// locks the API, and the wizard signs in on the spot so everything after —
// including the Kindle step, which needs an owner for its outgoing email —
// runs as that admin. Every OTHER field is an ordinary setting: "Next" saves
// what's filled in, "Skip" saves nothing, and anything can be revisited in
// Settings.
export function SetupWizard({ open, onOpenChange, onDone }: { open: boolean; onOpenChange: (v: boolean) => void; onDone?: () => void }) {
  const [step, setStep] = useState(0)
  const [busy, setBusy] = useState(false)
  const last = step === STEPS.length - 1

  // an account may already exist (re-run from Settings → About) — then the
  // mandatory step is already satisfied and shows as done
  const [accountExists, setAccountExists] = useState(false)
  useEffect(() => {
    if (!open) return
    delivery.me().then(r => setAccountExists(r.authRequired)).catch(() => setAccountExists(false))
  }, [open])

  // step state
  const [libs, setLibs] = useState<{ name: string; path: string }[]>([{ name: "", path: "/data/books/" }])
  const [naming, setNaming] = useState("{Author}/{Title}")
  const [hcToken, setHcToken] = useState("")
  const [writeOnImport, setWriteOnImport] = useState(true)
  const [rewriteOnRefresh, setRewriteOnRefresh] = useState(true)
  // formats hold their click order = preference order; cutoff must be one of them
  const [formats, setFormats] = useState<string[]>(["epub", "azw3", "mobi"])
  const [cutoff, setCutoff] = useState("epub")
  const toggleFormat = (f: string) => {
    setFormats(prev => {
      const next = prev.includes(f) ? prev.filter(x => x !== f) : [...prev, f]
      if (!next.includes(cutoff) && next.length > 0) setCutoff(next[0])
      return next
    })
  }
  const [prowlarrUrl, setProwlarrUrl] = useState("")
  const [prowlarrKey, setProwlarrKey] = useState("")
  const [sabUrl, setSabUrl] = useState("")
  const [sabKey, setSabKey] = useState("")
  const [sabCat, setSabCat] = useState("booky")
  const [zlibEmail, setZlibEmail] = useState("")
  const [zlibPassword, setZlibPassword] = useState("")
  const [annasKey, setAnnasKey] = useState("")
  const [downloadsDir, setDownloadsDir] = useState("/data/downloads/booky")
  const [grUrl, setGrUrl] = useState("")
  const [grShelves, setGrShelves] = useState<{ name: string; count: number }[] | null>(null)
  const [grPicked, setGrPicked] = useState<Set<string>>(new Set())
  const [grFinding, setGrFinding] = useState(false)
  const [hcLists, setHcLists] = useState<{ id: string; name: string; count: number }[] | null>(null)
  const [hcPicked, setHcPicked] = useState<Set<string>>(new Set())
  const [hcFinding, setHcFinding] = useState(false)
  const [listScope, setListScope] = useState("book")
  const [prowlarrOn, setProwlarrOn] = useState(true)
  const [zlibOn, setZlibOn] = useState(true)
  const [annasOn, setAnnasOn] = useState(true)
  const [adminUser, setAdminUser] = useState("")
  const [adminPass, setAdminPass] = useState("")
  const [serverUrl, setServerUrl] = useState("")

  // Send to Kindle step: the signed-in admin's own outgoing email + first
  // device. Libraries are fetched on entry — step 3 may have just made them.
  const [kFrom, setKFrom] = useState("")
  const [kHost, setKHost] = useState("")
  const [kPort, setKPort] = useState("587")
  const [kSecurity, setKSecurity] = useState("starttls")
  const [kUser, setKUser] = useState("")
  const [kPass, setKPass] = useState("")
  const [kDevName, setKDevName] = useState("")
  const [kDevEmail, setKDevEmail] = useState("")
  const [kSel, setKSel] = useState<Set<number>>(new Set())
  const [kAuto, setKAuto] = useState<Set<number>>(new Set())
  const [kLibs, setKLibs] = useState<{ id: number; name: string }[]>([])
  const [kTesting, setKTesting] = useState(false)
  useEffect(() => {
    if (!open || STEPS[step] !== "Send to Kindle") return
    api.libraries().then(r => setKLibs((r.libraries ?? []).map(l => ({ id: l.id, name: l.name })))).catch(() => setKLibs([]))
  }, [open, step])
  const kToggle = (set: Set<number>, setter: (s: Set<number>) => void, id: number) => {
    const next = new Set(set)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setter(next)
  }
  const kSaveSmtp = async () => {
    await kindle.putSmtp({
      fromAddr: kFrom.trim(), host: kHost.trim(), port: Number(kPort) || 587,
      security: kSecurity as "starttls" | "tls" | "none", username: kUser.trim(), password: kPass,
    })
  }
  const kTest = async () => {
    setKTesting(true)
    try {
      await kSaveSmtp()
      await kindle.testSmtp(kDevEmail.trim() || kFrom.trim())
      toast.success(`Test sent to ${kDevEmail.trim() || kFrom.trim()}`)
    } catch (e) {
      toast.error(`Test failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setKTesting(false)
    }
  }

  const close = () => { onOpenChange(false); setStep(0) }

  // apply saves the current step's non-empty fields; empty means "skip".
  const apply = async (): Promise<boolean> => {
    switch (step) {
      case 2: {
        for (const l of libs) {
          if (l.name.trim() && l.path.trim() && l.path.trim() !== "/data/books/") {
            await api.createLibrary(l.name.trim(), l.path.trim())
          }
        }
        if (naming.trim()) await api.putSetting("naming_scheme", naming.trim())
        return true
      }
      case 3: {
        if (hcToken.trim()) await api.putSetting("hardcover_token", hcToken.trim())
        await api.putSetting("write_on_import", writeOnImport ? "true" : "false")
        await api.putSetting("rewrite_on_refresh", rewriteOnRefresh ? "true" : "false")
        return true
      }
      case 4: {
        if (formats.length > 0) {
          const profiles = (await acquisition.profiles()).profiles ?? []
          if (profiles.length > 0) {
            await acquisition.updateProfile(profiles[0].id, {
              name: profiles[0].name, formats,
              cutoffFormat: formats.includes(cutoff) ? cutoff : formats[0],
              preferredTerms: profiles[0].preferredTerms, avoidedTerms: profiles[0].avoidedTerms,
            })
          }
        }
        return true
      }
      case 5: {
        if (prowlarrUrl.trim()) await api.putSetting("prowlarr_url", prowlarrUrl.trim())
        if (prowlarrKey.trim()) await api.putSetting("prowlarr_api_key", prowlarrKey.trim())
        await api.putSetting("prowlarr_enabled", prowlarrOn ? "true" : "false")
        return true
      }
      case 6: {
        if (sabUrl.trim()) await api.putSetting("sab_url", sabUrl.trim())
        if (sabKey.trim()) await api.putSetting("sab_api_key", sabKey.trim())
        if (sabCat.trim()) await api.putSetting("sab_category", sabCat.trim())
        return true
      }
      case 7: {
        // Mirrors ship with working defaults, so only credentials/keys here.
        // left at the default = keep the setting empty, so the built-in
        // default keeps applying even if it ever changes
        if (downloadsDir.trim() && downloadsDir.trim() !== "/data/downloads/booky") {
          await api.putSetting("downloads_dir", downloadsDir.trim())
        }
        if (zlibEmail.trim()) await api.putSetting("zlib_email", zlibEmail.trim())
        if (zlibPassword) await api.putSetting("zlib_password", zlibPassword)
        if (annasKey.trim()) await api.putSetting("annas_key", annasKey.trim())
        await api.putSetting("zlib_enabled", zlibOn ? "true" : "false")
        await api.putSetting("annas_enabled", annasOn ? "true" : "false")
        return true
      }
      case 8: {
        if (!grUrl.trim() && hcPicked.size === 0) {
          return true // nothing entered = skip
        }
        if (grUrl.trim() && grPicked.size === 0) {
          toast.error("Click \"Find shelves\" and pick at least one — or clear the URL")
          return false
        }
        const libs = (await api.libraries()).libraries ?? []
        if (libs.length === 0) {
          toast.error("Create a library first (step 3) — lists route their books into one")
          return false
        }
        const common = {
          libraryId: libs[0].id, monitorScope: listScope, onRemove: "nothing",
          searchOnAdd: true, enabled: true,
        }
        for (const shelf of grPicked) {
          await watchers.createList({
            ...common, name: `Goodreads ${shelf}`, kind: "goodreads_rss",
            sourceRef: grUrl.trim(), shelf,
          })
        }
        for (const id of hcPicked) {
          const list = hcLists?.find(l => l.id === id)
          await watchers.createList({
            ...common, name: list ? list.name : `Hardcover ${id}`, kind: "hardcover",
            sourceRef: id,
          })
        }
        return true
      }
      case 1: {
        // the one mandatory step — the Next button won't enable without
        // credentials, and there's no Skip until an account exists
        if (accountExists) {
          return true
        }
        await delivery.createUser(adminUser.trim(), adminPass, "admin")
        // the API locks the moment a user exists — sign in right away so
        // the rest of the wizard (and the app) keeps working as this admin
        await delivery.login(adminUser.trim(), adminPass)
        setAccountExists(true)
        return true
      }
      case 9: {
        if (serverUrl.trim()) await api.putSetting("server_url", serverUrl.trim())
        return true
      }
      case 10: {
        // both halves optional — filled-in outgoing email and/or a first
        // device, owned by the admin from step 1
        if (kFrom.trim() && kHost.trim()) {
          await kSaveSmtp()
        }
        if (kDevName.trim() && kDevEmail.trim() && kSel.size > 0) {
          await kindle.createDevice(kDevName.trim(), kDevEmail.trim(), [...kSel], [...kAuto].filter(id => kSel.has(id)))
        }
        return true
      }
      default:
        return true
    }
  }

  const next = async () => {
    setBusy(true)
    try {
      if (!(await apply())) return
      if (last) {
        toast.success("Setup complete — Booky is watching your lists")
        close()
        onDone?.()
      } else {
        setStep(s => s + 1)
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const testProwlarr = async () => {
    try {
      const r = await acquisition.testProwlarr(prowlarrUrl.trim(), prowlarrKey.trim())
      toast.success(`Connected — Prowlarr ${r.version}, ${r.indexers} indexers`)
    } catch (e) {
      toast.error(`Connection failed: ${e instanceof Error ? e.message : e}`)
    }
  }

  const testSab = async () => {
    try {
      const r = await acquisition.testSab(sabUrl.trim(), sabKey.trim(), sabCat.trim())
      toast.success(`Connected — SABnzbd ${r.version}`)
    } catch (e) {
      toast.error(`Connection failed: ${e instanceof Error ? e.message : e}`)
    }
  }

  const testZlib = async () => {
    try {
      // the test endpoint reads saved settings, so persist first
      await api.putSetting("zlib_email", zlibEmail.trim())
      await api.putSetting("zlib_password", zlibPassword)
      const r = await acquisition.testZlib()
      toast.success(`Connected — ${r.downloadsLeft} of ${r.downloadsLimit} downloads left today`)
    } catch (e) {
      toast.error(`Connection failed: ${e instanceof Error ? e.message : e}`)
    }
  }

  const testHardcover = async () => {
    try {
      await acquisition.testHardcover(hcToken.trim())
      toast.success("Connected — Hardcover token works")
    } catch (e) {
      toast.error(`Token failed: ${e instanceof Error ? e.message : e}`)
    }
  }

  const findHcLists = async () => {
    setHcFinding(true)
    try {
      // the token from the metadata step may not be saved yet if that step
      // was skipped past — save it so discovery can use it
      if (hcToken.trim()) await api.putSetting("hardcover_token", hcToken.trim())
      const r = await acquisition.discoverHardcover()
      setHcLists(r.lists)
      setHcPicked(new Set())
      if (r.lists.length === 0) toast("No lists on this Hardcover account yet")
    } catch (e) {
      toast.error(`Couldn't load lists — is the Hardcover token set (step 4)? ${e instanceof Error ? e.message : e}`)
    } finally {
      setHcFinding(false)
    }
  }

  const findGrShelves = async () => {
    setGrFinding(true)
    try {
      const r = await acquisition.discoverGoodreads(grUrl.trim())
      setGrShelves(r.shelves)
      setGrPicked(new Set(["to-read"]))
      toast.success(`Found the account — pick the shelves to watch`)
    } catch (e) {
      toast.error(`Couldn't find that profile: ${e instanceof Error ? e.message : e}`)
    } finally {
      setGrFinding(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={v => { onOpenChange(v); if (!v) setStep(0) }}>
      <DialogContent className="max-w-lg gap-0 p-0">
        <div className="border-b px-6 py-5">
          <div className="flex items-baseline justify-between">
            <div className="mono-label text-faint">First-run setup · step {step + 1} of {STEPS.length}</div>
            {/* whole-wizard escape hatch — only once the mandatory account exists */}
            {accountExists && (
              <button
                className="mono-label text-faint hover:text-brass"
                onClick={() => { toast("Setup skipped — everything here also lives in Settings"); close() }}
              >
                Skip setup
              </button>
            )}
          </div>
          <h2 className="font-book mt-1 text-[22px] font-bold">{STEPS[step]}</h2>
          <div className="mt-3 flex gap-1.5">
            {STEPS.map((s, i) => (
              <span key={s} className={cn("h-1 flex-1 rounded-full", i <= step ? "bg-brass" : "bg-surface3")} />
            ))}
          </div>
        </div>

        <div className="max-h-[340px] min-h-[240px] overflow-y-auto px-6 py-5">
          {step === 0 && (
            <div className="text-[13.5px] leading-relaxed text-muted-foreground">
              <p className="mb-3">Welcome to <span className="font-book font-bold italic text-foreground">Book<span className="text-brass">y</span></span>. This wizard walks through the whole setup, in order — your account first, then libraries, metadata, quality, sources, downloads, lists, and your e-readers.</p>
              <p>Creating your admin account is the one required step. Everything after it can be skipped and finished later in Settings — "Next" saves what you've filled in.</p>
            </div>
          )}
          {step === 1 && (
            <div className="grid gap-4">
              {accountExists ? (
                <p className="text-[13.5px] leading-relaxed text-muted-foreground">
                  An admin account already exists and you're signed in — nothing to do here.
                </p>
              ) : (
                <>
                  <p className="text-[13px] text-muted-foreground">Booky locks itself the moment this account exists — everything else in setup is done signed in as you. E-readers never use these credentials.</p>
                  <Field label="Admin username / password (min 8 chars)">
                    <div className="flex gap-2">
                      <Input value={adminUser} onChange={e => setAdminUser(e.target.value)} placeholder="username" className="max-w-[140px]" />
                      <Input type="password" value={adminPass} onChange={e => setAdminPass(e.target.value)} placeholder="password" />
                    </div>
                  </Field>
                  <p className="text-xs text-faint">More accounts (and per-library scopes) live in Settings → Users.</p>
                </>
              )}
            </div>
          )}
          {step === 2 && (
            <div className="grid gap-4">
              <p className="text-[13px] text-muted-foreground">Add a root folder per library. Keep them on the same filesystem as your downloads for instant hardlink imports.</p>
              {libs.map((l, i) => (
                <Field key={i} label={i === 0 ? "Library · name / path" : `Library ${i + 1} · name / path`}>
                  <div className="flex items-center gap-2">
                    <Input value={l.name} placeholder={i === 0 ? "Library name" : "Another library"} className="max-w-[160px]"
                      onChange={e => setLibs(ls => ls.map((x, j) => j === i ? { ...x, name: e.target.value } : x))} />
                    <div className="flex-1">
                      <PathInput value={l.path}
                        onChange={v => setLibs(ls => ls.map((x, j) => j === i ? { ...x, path: v } : x))} />
                    </div>
                    {i > 0 && (
                      <button type="button" aria-label="Remove library row"
                        className="mono-label text-faint hover:text-want"
                        onClick={() => setLibs(ls => ls.filter((_, j) => j !== i))}>✕</button>
                    )}
                  </div>
                </Field>
              ))}
              <button type="button"
                className="mono-label flex w-fit items-center gap-1.5 text-muted-foreground hover:text-brass"
                onClick={() => setLibs(ls => [...ls, { name: "", path: "/data/books/" }])}>
                + Add another library
              </button>
              <Field label="File naming"><Input value={naming} onChange={e => setNaming(e.target.value)} className="max-w-[240px] font-label text-[11.5px]" /></Field>
            </div>
          )}
          {step === 3 && (
            <div className="grid gap-4">
              <p className="text-[13px] text-muted-foreground">Metadata comes from providers in priority order — first one with a value wins, the rest fill gaps. Goodreads and Open Library need no keys; add a Hardcover token for its clean series data.</p>
              <Field label="Hardcover API token (optional)">
                <Input type="password" value={hcToken} onChange={e => setHcToken(e.target.value)} placeholder="paste token from hardcover.app → settings → API" />
              </Field>
              <Button variant="outline" className="h-8 w-fit" disabled={!hcToken.trim()} onClick={testHardcover}>Test token</Button>
              <label className="flex items-center justify-between gap-4 text-[13px]">
                <span>Write metadata into files on import <span className="text-faint">(title, author, series, cover, identifiers…)</span></span>
                <Switch checked={writeOnImport} onCheckedChange={setWriteOnImport} />
              </label>
              <label className="flex items-center justify-between gap-4 text-[13px]">
                <span>Rewrite files when metadata improves upstream</span>
                <Switch checked={rewriteOnRefresh} onCheckedChange={setRewriteOnRefresh} />
              </label>
            </div>
          )}
          {step === 4 && (
            <div className="grid gap-4">
              <p className="text-[13px] text-muted-foreground">Pick the formats to grab — the order you pick them is the order of preference. The cutoff is where upgrading stops.</p>
              <Field label="Allowed formats · click in order of preference">
                <div className="flex flex-wrap gap-1.5">
                  {["epub", "azw3", "mobi", "pdf", "cbz"].map(f => {
                    const idx = formats.indexOf(f)
                    return (
                      <button key={f} type="button" onClick={() => toggleFormat(f)}
                        className={cn(
                          "font-label rounded-md border px-2.5 py-1.5 text-[11.5px] uppercase tracking-[0.06em]",
                          idx >= 0 ? "border-brass bg-brass/15 text-brass" : "text-muted-foreground hover:border-brass/50"
                        )}>
                        {idx >= 0 && <span className="mr-1.5 font-bold">{idx + 1}</span>}{f}
                      </button>
                    )
                  })}
                </div>
              </Field>
              <Field label="Cutoff — stop upgrading at">
                <select value={formats.includes(cutoff) ? cutoff : formats[0] ?? ""}
                  onChange={e => setCutoff(e.target.value)} disabled={formats.length === 0}
                  className="h-9 max-w-[140px] rounded-md border border-input bg-transparent px-2.5 font-label text-[11.5px] uppercase">
                  {formats.map(f => <option key={f} value={f}>{f}</option>)}
                </select>
              </Field>
              <p className="text-xs text-faint">This updates the default profile, assigned to every library. Fine-tune in Settings → Quality profiles.</p>
            </div>
          )}
          {step === 5 && (
            <div className="grid gap-4">
              <p className="text-[13px] text-muted-foreground">Point Booky at Prowlarr and your indexers sync automatically. This is optional — the next steps set up SABnzbd and the direct-download providers too.</p>
              <label className="flex w-fit cursor-pointer items-center gap-2.5 text-[13px]">
                <Switch checked={prowlarrOn} onCheckedChange={setProwlarrOn} /> Use Prowlarr for searches
              </label>
              <Field label="Prowlarr URL"><Input value={prowlarrUrl} onChange={e => setProwlarrUrl(e.target.value)} placeholder="http://prowlarr:9696" /></Field>
              <Field label="API key"><Input type="password" value={prowlarrKey} onChange={e => setProwlarrKey(e.target.value)} placeholder="••••••••••••••••" /></Field>
              <Button variant="outline" className="h-8 w-fit" disabled={!prowlarrUrl.trim()} onClick={testProwlarr}>Test connection</Button>
            </div>
          )}
          {step === 6 && (
            <div className="grid gap-4">
              <p className="text-[13px] text-muted-foreground">Usenet downloads go through SABnzbd under a dedicated category. Its completed folder must live under /data for hardlink imports.</p>
              <Field label="SABnzbd URL"><Input value={sabUrl} onChange={e => setSabUrl(e.target.value)} placeholder="http://sabnzbd:8080" /></Field>
              <div className="grid grid-cols-2 gap-3">
                <Field label="API key"><Input type="password" value={sabKey} onChange={e => setSabKey(e.target.value)} placeholder="••••••••••••" /></Field>
                <Field label="Category"><Input value={sabCat} onChange={e => setSabCat(e.target.value)} className="font-label text-[11.5px]" /></Field>
              </div>
              <Button variant="outline" className="h-8 w-fit" disabled={!sabUrl.trim()} onClick={testSab}>Test connection</Button>
            </div>
          )}
          {step === 7 && (
            <div className="grid gap-4">
              <p className="text-[13px] text-muted-foreground">Direct-download providers grab books straight over HTTP — no indexer or download client needed. Both are optional and ship with working mirror lists (edit them in Settings → Sources). Turn off any you don't want searched — no Z-Library account, or Anna's free servers misbehaving.</p>
              <Field label="Downloads folder">
                <PathInput value={downloadsDir} onChange={setDownloadsDir} placeholder="/data/downloads/booky (default)" />
                <p className="mt-1.5 text-xs text-faint">Where downloads land before import — must be under /data. This is the default.</p>
              </Field>
              <label className="flex w-fit cursor-pointer items-center gap-2.5 text-[13px]">
                <Switch checked={zlibOn} onCheckedChange={setZlibOn} /> Use Z-Library
              </label>
              <Field label="Z-Library — email">
                <Input value={zlibEmail} onChange={e => setZlibEmail(e.target.value)} placeholder="you@example.com" />
              </Field>
              <Field label="Z-Library — password">
                <Input type="password" value={zlibPassword} onChange={e => setZlibPassword(e.target.value)} placeholder="your Z-Library password" />
              </Field>
              <Button variant="outline" className="h-8 w-fit" disabled={!zlibEmail.trim() || !zlibPassword} onClick={testZlib}>Test Z-Library login</Button>
              <label className="flex w-fit cursor-pointer items-center gap-2.5 text-[13px]">
                <Switch checked={annasOn} onCheckedChange={setAnnasOn} /> Use Anna's Archive
              </label>
              <Field label="Anna's Archive — member key (optional)">
                <Input type="password" value={annasKey} onChange={e => setAnnasKey(e.target.value)} placeholder="from a paid membership — leave blank for free slow downloads" className="font-label text-[11.5px]" />
              </Field>
              <p className="text-xs text-faint">A key is always safe to add: fast downloads when your membership is active, the free path when it isn't.</p>
            </div>
          )}
          {step === 8 && (
            <div className="grid gap-4">
              <p className="text-[13px] text-muted-foreground">Watch your Goodreads shelves and Hardcover lists — new books on them are grabbed automatically, into your first library. More lists and options live in Settings → Watched lists.</p>
              <Field label="Goodreads profile URL or ID">
                <div className="flex gap-2">
                  <Input value={grUrl} onChange={e => { setGrUrl(e.target.value); setGrShelves(null); setGrPicked(new Set()) }}
                    placeholder="goodreads.com/user/show/12345678" className="font-label text-[11.5px]" />
                  <Button variant="outline" className="h-9 shrink-0" disabled={grFinding || !grUrl.trim()} onClick={findGrShelves}>
                    {grFinding ? "Looking…" : "Find shelves"}
                  </Button>
                </div>
              </Field>
              {grShelves && (
                <Field label="Watch these shelves">
                  <div className="flex flex-wrap gap-x-5 gap-y-1.5">
                    {grShelves.map(s => (
                      <label key={s.name} className="flex cursor-pointer items-center gap-2 text-[13px]">
                        <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]"
                          checked={grPicked.has(s.name)}
                          onChange={() => setGrPicked(prev => {
                            const next = new Set(prev)
                            if (next.has(s.name)) next.delete(s.name)
                            else next.add(s.name)
                            return next
                          })} />
                        {s.name}
                        {s.count >= 0 && <span className="text-xs text-faint">({s.count})</span>}
                      </label>
                    ))}
                  </div>
                </Field>
              )}
              <Field label="Hardcover lists (needs the API token from step 3)">
                <Button variant="outline" className="h-9 w-fit" disabled={hcFinding} onClick={findHcLists}>
                  {hcFinding ? "Looking…" : "Load my lists"}
                </Button>
              </Field>
              {hcLists && hcLists.length > 0 && (
                <Field label="Watch these lists">
                  <div className="flex flex-col gap-1.5">
                    {hcLists.map(l => (
                      <label key={l.id} className="flex cursor-pointer items-center gap-2 text-[13px]">
                        <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]"
                          checked={hcPicked.has(l.id)}
                          onChange={() => setHcPicked(prev => {
                            const next = new Set(prev)
                            if (next.has(l.id)) next.delete(l.id)
                            else next.add(l.id)
                            return next
                          })} />
                        <span className="font-medium">{l.name}</span>
                        <span className="text-xs text-faint">{l.count} books</span>
                      </label>
                    ))}
                  </div>
                </Field>
              )}
              {(grPicked.size > 0 || hcPicked.size > 0) && (
                <Field label="When a book appears on a list, monitor">
                  <select value={listScope} onChange={e => setListScope(e.target.value)}
                    className="h-9 max-w-[220px] rounded-md border border-input bg-transparent px-2.5 text-[12.5px]">
                    <option value="book">Just the listed book</option>
                    <option value="series">The whole series</option>
                    <option value="author">The author's backlist too</option>
                  </select>
                </Field>
              )}
            </div>
          )}
          {step === 9 && (
            <div className="grid gap-4">
              <div className="text-[13.5px] leading-relaxed text-muted-foreground">
                <p className="mb-3">Put Booky on your e-readers. Build a preconfigured KoReader plugin per device under <b className="text-foreground">Settings → KoReader devices</b> — pick which libraries it can browse and which auto-download, drop the zip in <span className="font-label text-[11px]">plugins/</span>, done.</p>
              </div>
              <Field label="Server URL (baked into plugin zips — an address your devices can reach)">
                <Input value={serverUrl} onChange={e => setServerUrl(e.target.value)} placeholder="http://192.168.1.10:8787" className="font-label text-[11.5px]" />
              </Field>
              <p className="text-xs text-faint">That's the whole loop: add a book to a list, it's on the device minutes later.</p>
            </div>
          )}
          {step === 10 && (
            <div className="grid gap-4">
              <p className="text-[13px] text-muted-foreground">Booky can email books straight to a Kindle. This sets up <em>your</em> sending account and first device — everyone else adds theirs in Settings → Send to Kindle.</p>
              <Field label="From address">
                <Input value={kFrom} onChange={e => setKFrom(e.target.value)} placeholder="you@gmail.com" />
                <p className="mt-1 text-xs text-faint">Each Kindle must have this address on its approved sender list (Amazon → Preferences → Personal Document Settings).</p>
              </Field>
              <div className="grid grid-cols-2 gap-3">
                <Field label="SMTP server"><Input value={kHost} onChange={e => setKHost(e.target.value)} placeholder="smtp.gmail.com" /></Field>
                <Field label="Port · security">
                  <div className="flex gap-2">
                    <Input value={kPort} onChange={e => setKPort(e.target.value)} inputMode="numeric" className="w-[70px]" />
                    <select value={kSecurity} onChange={e => setKSecurity(e.target.value)}
                      className="h-9 flex-1 rounded-md border border-input bg-transparent px-2 text-[12.5px]">
                      <option value="starttls">STARTTLS</option>
                      <option value="tls">TLS (465)</option>
                      <option value="none">None</option>
                    </select>
                  </div>
                </Field>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <Field label="Username"><Input value={kUser} onChange={e => setKUser(e.target.value)} placeholder="you@gmail.com" /></Field>
                <Field label="Password"><Input type="password" value={kPass} onChange={e => setKPass(e.target.value)} placeholder="app password" /></Field>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <Field label="First Kindle — name"><Input value={kDevName} onChange={e => setKDevName(e.target.value)} placeholder="My Paperwhite" /></Field>
                <Field label="Kindle email"><Input value={kDevEmail} onChange={e => setKDevEmail(e.target.value)} placeholder="name_xxxx@kindle.com" /></Field>
              </div>
              {kLibs.length > 0 && (
                <Field label="Libraries (check auto-send to email new arrivals)">
                  <div className="flex flex-col gap-1.5">
                    {kLibs.map(l => (
                      <div key={l.id} className="flex items-center gap-4 text-[13px]">
                        <label className="flex w-[160px] cursor-pointer items-center gap-2">
                          <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]"
                            checked={kSel.has(l.id)} onChange={() => kToggle(kSel, setKSel, l.id)} />
                          <span className="font-medium">{l.name}</span>
                        </label>
                        <label className="flex cursor-pointer items-center gap-1.5 text-[12px] text-muted-foreground">
                          <input type="checkbox" className="h-3.5 w-3.5 accent-[hsl(var(--brass))]" disabled={!kSel.has(l.id)}
                            checked={kAuto.has(l.id) && kSel.has(l.id)} onChange={() => kToggle(kAuto, setKAuto, l.id)} />
                          auto-send
                        </label>
                      </div>
                    ))}
                  </div>
                </Field>
              )}
              <Button variant="outline" className="h-8 w-fit" disabled={kTesting || !kFrom.trim() || !kHost.trim()} onClick={kTest}>
                {kTesting ? "Sending…" : "Send test email"}
              </Button>
            </div>
          )}
        </div>

        <div className="flex items-center justify-between border-t px-6 py-4">
          <Button variant="outline" className="" disabled={step === 0 || busy} onClick={() => setStep(s => s - 1)}>Back</Button>
          <div className="flex gap-2">
            {/* the admin step is the one with no Skip — everything after has one */}
            {!last && !(step === 1 && !accountExists) && (
              <Button variant="outline" className="text-faint" disabled={busy} onClick={() => setStep(s => s + 1)}>Skip</Button>
            )}
            <Button className=""
              disabled={busy || (step === 1 && !accountExists && (!adminUser.trim() || adminPass.length < 8))}
              onClick={next}>
              {busy ? "Saving…" : last ? "Finish" : step === 1 && !accountExists ? "Create account & continue" : "Next"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
