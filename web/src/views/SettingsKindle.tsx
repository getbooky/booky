import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tag } from "@/components/bits"
import { kindle } from "@/api"
import type { ApiKindleDevice, ApiSmtpConfig, ApiTag } from "@/api"
import { TagPicker, useTags } from "@/components/TagPills"
import { useAccess } from "@/lib/access"
import { formatFull, formatWhen } from "@/lib/time"
import { useApi } from "@/hooks/use-api"
import { toast } from "sonner"
import { Pencil, Plus, X } from "lucide-react"

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

function Label({ children }: { children: React.ReactNode }) {
  return <div className="mono-label mb-1.5 text-muted-foreground">{children}</div>
}

/* ---------- Kindle devices ---------- */

// AUTO_SEND_HINT is the rule, said once: what auto-send does and doesn't do.
const AUTO_SEND_HINT = "A book is emailed once, the moment it's on the shelf and carries one of these tags — when it imports, or when it's tagged later. Adding a tag here doesn't email the books that already have it. Send to Kindle on a book's page works for anything you can see."

function KindleDeviceRow({ device, tags, allowed, keepPersonal, onChanged }: {
  device: ApiKindleDevice
  tags: ApiTag[]
  allowed?: number[]
  keepPersonal: number
  onChanged: () => void
}) {
  const byId = new Map(tags.map(t => [t.id, t]))
  const [testing, setTesting] = useState(false)
  // in-place editing: change auto-send tags, fix the address — no re-pairing
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState(device.name)
  const [email, setEmail] = useState(device.email)
  const [auto, setAuto] = useState<number[]>(device.autoTagIds)
  const startEdit = () => {
    setName(device.name); setEmail(device.email)
    setAuto(device.autoTagIds)
    setEditing(true)
  }
  const save = async () => {
    try {
      await kindle.updateDevice(device.id, name.trim(), email.trim(), auto)
      toast.success(`"${name.trim()}" updated`)
      setEditing(false)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  const remove = async () => {
    if (!window.confirm(`Remove "${device.name}"? Nothing more is sent to it.`)) return
    try {
      await kindle.removeDevice(device.id)
      toast(`Device "${device.name}" removed`)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't remove: ${e instanceof Error ? e.message : e}`)
    }
  }
  const test = async () => {
    setTesting(true)
    try {
      await kindle.testDevice(device.id)
      toast.success(`Test sent to ${device.email} — check the Kindle's Docs`)
    } catch (e) {
      toast.error(`Test failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setTesting(false)
    }
  }
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border border-linesoft bg-surface2 px-4 py-3">
      <div className="min-w-[180px]">
        <div className="font-book text-[15px] font-bold">{device.name}</div>
        <div className="font-label text-[11px] text-muted-foreground">{device.email}</div>
        <div className="text-[11.5px] text-faint" title={formatFull(device.lastSent)}>
          {device.lastSent ? `last sent ${formatWhen(device.lastSent)}` : "never sent"}
        </div>
      </div>
      {/* only admins are sent an owner name, and only they see other
          people's devices — so this labels exactly the rows that need it */}
      {device.ownerName && <Tag kind="dim">{device.ownerName}</Tag>}
      {device.autoTagIds.length === 0
        ? <span className="text-[12px] text-faint">manual sends only</span>
        : device.autoTagIds.map(id => (
            <Tag key={id} kind="info">auto · {byId.get(id)?.name ?? `#${id}`}</Tag>
          ))}
      {!device.emailConfigured && (
        <Tag kind="want">owner email not set</Tag>
      )}
      <div className="ml-auto flex gap-2">
        <Button variant="outline" className="h-8" disabled={testing || !device.emailConfigured} onClick={test}>
          Send test
        </Button>
        <Button variant="outline" size="icon" className="h-8 w-8" aria-label={`Edit ${device.name}`}
          onClick={() => (editing ? setEditing(false) : startEdit())}>
          <Pencil className="h-3.5 w-3.5" />
        </Button>
        <Button variant="outline" size="icon" className="h-8 w-8 text-faint" aria-label={`Remove ${device.name}`} onClick={remove}>
          <X className="h-3.5 w-3.5" />
        </Button>
      </div>
      {editing && (
        <div className="w-full rounded-lg border border-dashed border-linesoft px-4 py-3">
          <div className="flex flex-wrap items-end gap-3">
            <div>
              <Label>Device name</Label>
              <Input value={name} onChange={e => setName(e.target.value)} className="h-9 w-[180px]" />
            </div>
            <div>
              <Label>Kindle email</Label>
              <Input value={email} onChange={e => setEmail(e.target.value)} className="h-9 w-[240px]" />
            </div>
          </div>
          <div className="mt-3">
            <Label>Auto-send books tagged</Label>
            <TagPicker tags={tags} allowed={allowed} hidePersonal keepPersonal={keepPersonal} value={auto} onChange={setAuto} />
            <p className="mt-1.5 max-w-[62ch] text-xs text-faint">{AUTO_SEND_HINT}</p>
          </div>
          <div className="mt-3 flex gap-2">
            <Button className="h-9" disabled={!name.trim() || !email.trim()} onClick={save}>Save</Button>
            <Button variant="outline" className="h-9" onClick={() => setEditing(false)}>Cancel</Button>
          </div>
        </div>
      )}
    </div>
  )
}

/* ---------- Outgoing email ---------- */

function OutgoingEmailCard({ configured, onChanged }: { configured: boolean; onChanged: () => void }) {
  const { data, reload } = useApi(() => kindle.smtp())
  const cfg = data?.config
  const [draft, setDraft] = useState<Omit<ApiSmtpConfig, "passwordSet"> | null>(null)
  const [password, setPassword] = useState("")
  const [editingPassword, setEditingPassword] = useState(false)
  const [testTo, setTestTo] = useState("")
  const [busy, setBusy] = useState(false)
  const form = draft ?? {
    fromAddr: cfg?.fromAddr ?? "", host: cfg?.host ?? "", port: cfg?.port ?? 587,
    security: cfg?.security ?? "starttls", username: cfg?.username ?? "",
  }
  const edit = (patch: Partial<typeof form>) => setDraft({ ...form, ...patch })

  const save = async () => {
    setBusy(true)
    try {
      await kindle.putSmtp({ ...form, password })
      toast.success("Outgoing email saved")
      setDraft(null); setPassword(""); setEditingPassword(false)
      reload(); onChanged()
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    } finally {
      setBusy(false)
    }
  }
  const test = async () => {
    if (!testTo.trim()) return
    setBusy(true)
    try {
      await kindle.testSmtp(testTo.trim())
      toast.success(`Test sent to ${testTo.trim()}`)
    } catch (e) {
      toast.error(`Test failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setBusy(false)
    }
  }
  const clear = async () => {
    if (!window.confirm("Remove your outgoing email? Your devices stop receiving until it's set up again.")) return
    try {
      await kindle.clearSmtp()
      toast("Outgoing email removed")
      setDraft(null); setPassword(""); setEditingPassword(false)
      reload(); onChanged()
    } catch (e) {
      toast.error(`${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <Card title="Outgoing email"
      action={configured ? <Tag kind="good">configured</Tag> : <Tag kind="dim">not set</Tag>}
      desc="The account your books are sent from — required before your devices can receive anything. Use an app password, not your real one.">
      <div className="grid max-w-xl gap-4">
        <div>
          <Label>From address</Label>
          <Input value={form.fromAddr} onChange={e => edit({ fromAddr: e.target.value })} placeholder="you@gmail.com" />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label>SMTP server</Label>
            <Input value={form.host} onChange={e => edit({ host: e.target.value })} placeholder="smtp.gmail.com" />
          </div>
          <div>
            <Label>Port · security</Label>
            <div className="flex gap-2">
              <Input value={String(form.port)} inputMode="numeric" className="w-[76px]"
                onChange={e => edit({ port: Number(e.target.value) || 0 })} />
              <select className="h-9 flex-1 rounded-md border border-input bg-transparent px-2.5 text-[12.5px]"
                value={form.security} onChange={e => edit({ security: e.target.value as ApiSmtpConfig["security"] })}>
                <option value="starttls">STARTTLS</option>
                <option value="tls">TLS (implicit, 465)</option>
                <option value="none">None</option>
              </select>
            </div>
          </div>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label>Username</Label>
            <Input value={form.username} onChange={e => edit({ username: e.target.value })} placeholder="you@gmail.com" />
          </div>
          <div>
            <Label>Password</Label>
            {cfg?.passwordSet && !editingPassword ? (
              <div className="flex h-9 items-center justify-between rounded-md border border-input px-3 text-[13px] text-faint">
                <span className="tracking-widest">••••••••</span>
                <button type="button" className="text-[12.5px] text-brass" onClick={() => setEditingPassword(true)}>replace…</button>
              </div>
            ) : (
              <Input type="password" value={password} onChange={e => setPassword(e.target.value)}
                placeholder={cfg?.passwordSet ? "new app password" : "app password"} />
            )}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button className="h-9" disabled={busy || !form.fromAddr.trim() || !form.host.trim()} onClick={save}>Save</Button>
          <Input value={testTo} onChange={e => setTestTo(e.target.value)} placeholder="name_xxxx@kindle.com"
            className="h-9 max-w-[220px]" />
          <Button variant="outline" className="h-9" disabled={busy || !configured || !testTo.trim()} onClick={test}>
            Send test email
          </Button>
          {configured && (
            <Button variant="outline" className="h-9 text-faint" onClick={clear}>Remove</Button>
          )}
        </div>
      </div>
    </Card>
  )
}

/* ---------- panel ---------- */

export function KindlePanel() {
  const { isAdmin, tagIds, personalTagId } = useAccess()
  const { data, loading, reload } = useApi(() => kindle.devices())
  const smtp = useApi(() => kindle.smtp())
  // already scoped by the server: a user is offered only their own tags
  const { tags } = useTags()
  const allowed = isAdmin ? undefined : tagIds
  const devices = data?.devices ?? []
  const configured = smtp.data?.configured === true
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState("")
  const [email, setEmail] = useState("")
  const [auto, setAuto] = useState<number[]>([])

  const create = async () => {
    try {
      await kindle.createDevice(name.trim(), email.trim(), auto)
      toast.success(`Device "${name.trim()}" added`)
      setAdding(false); setName(""); setEmail(""); setAuto([])
      reload()
    } catch (e) {
      toast.error(`Couldn't add device: ${e instanceof Error ? e.message : e}`)
    }
  }
  const onChanged = () => { reload(); smtp.reload() }

  return (
    <>
      <Card title="Kindle Devices"
        action={
          <Button variant="outline" size="icon" className="h-7 w-7 rounded-full" aria-label="Add device" onClick={() => setAdding(a => !a)}>
            <Plus className="h-4 w-4" />
          </Button>
        }
        desc={
          isAdmin
            ? "Books are emailed to a Kindle's @kindle.com address — EPUB or PDF, up to 50 MB. The Kindle must approve your from address first (Amazon → Preferences → Personal Document Settings). Auto-send emails books carrying the tags you pick — each book once. You see every account's devices; each sends through its owner's outgoing email and never reaches past its owner's tags."
            : "Books are emailed to a Kindle's @kindle.com address — EPUB or PDF, up to 50 MB. The Kindle must approve your from address first (Amazon → Preferences → Personal Document Settings). Auto-send emails books carrying the tags you pick — each book once. Only your devices appear here."
        }>
        {!configured && !smtp.loading && (
          <div className="mb-3 rounded-r-lg border-l-[3px] border-want bg-want/10 px-4 py-2.5 text-[13px]">
            Set up your outgoing email below to turn on Send to Kindle.
          </div>
        )}
        {loading && <p className="mono-label text-faint">loading…</p>}
        {!loading && devices.length === 0 && !adding && (
          <p className="text-[13px] text-muted-foreground">
            {isAdmin ? "No devices yet." : "You haven't added a device yet."}
          </p>
        )}
        <div className="flex flex-col gap-2">
          {devices.map(d => (
            <KindleDeviceRow key={d.id} device={d} tags={tags} allowed={allowed} keepPersonal={personalTagId} onChanged={onChanged} />
          ))}
          {adding && (
            <div className="rounded-lg border border-dashed border-linesoft px-4 py-3">
              <div className="flex flex-wrap items-end gap-3">
                <div>
                  <Label>Device name</Label>
                  <Input value={name} onChange={e => setName(e.target.value)} className="h-9 w-[180px]" placeholder="My Paperwhite" />
                </div>
                <div>
                  <Label>Kindle email</Label>
                  <Input value={email} onChange={e => setEmail(e.target.value)} className="h-9 w-[240px]" placeholder="name_xxxx@kindle.com" />
                </div>
              </div>
              <div className="mt-3">
                <Label>Auto-send books tagged</Label>
                <TagPicker tags={tags} allowed={allowed} hidePersonal keepPersonal={personalTagId} value={auto} onChange={setAuto} />
                <p className="mt-1.5 max-w-[62ch] text-xs text-faint">{AUTO_SEND_HINT} Leave it empty for manual sends only.</p>
              </div>
              <div className="mt-3 flex gap-2">
                <Button className="h-9" disabled={!name.trim() || !email.trim()} onClick={create}>Add device</Button>
                <Button variant="outline" className="h-9" onClick={() => setAdding(false)}>Cancel</Button>
              </div>
            </div>
          )}
        </div>
      </Card>
      <OutgoingEmailCard configured={configured} onChanged={onChanged} />
    </>
  )
}
