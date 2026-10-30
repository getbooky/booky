import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Tag } from "@/components/bits"
import { api, delivery } from "@/api"
import type { ApiDevice, ApiTag, ApiUser } from "@/api"
import { useAccess } from "@/lib/access"
import { formatFull, formatWhen } from "@/lib/time"
import { useApi } from "@/hooks/use-api"
import { toast } from "sonner"
import { Download, Pencil, Plus, X } from "lucide-react"
import { SettingField } from "@/views/Settings"
import { TagPicker, TagPill, useTags } from "@/components/TagPills"

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

/* ---------- Users ---------- */

// UserRow shows the tags an account holds and lets an admin change its shared
// tags without deleting it. The personal tag is the account's own and shows
// read-only; admins see every book whatever they hold.
function UserRow({ user, tags, onDelete, onChanged }: {
  user: ApiUser
  tags: ApiTag[]
  onDelete: () => void
  onChanged: () => void
}) {
  const shared = (user.tagIds ?? []).filter(id => id !== user.personalTagId)
  const [editing, setEditing] = useState(false)
  const [selected, setSelected] = useState<number[]>(shared)
  const isAdminUser = user.role === "admin"
  const byId = new Map(tags.map(t => [t.id, t]))
  const personal = byId.get(user.personalTagId)

  const save = async () => {
    try {
      await delivery.setUserTags(user.id, selected)
      toast.success(`${user.username}'s tags updated — it applies on their next click`)
      setEditing(false)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  const opdsOff = async () => {
    if (!window.confirm(`Turn off ${user.username}'s OPDS login?\n\nReader apps using it stop working until they set a new OPDS password.`)) return
    try {
      await delivery.clearUserOPDS(user.id)
      toast(`${user.username}'s OPDS login turned off`)
    } catch (e) {
      toast.error(`Couldn't turn it off: ${e instanceof Error ? e.message : e}`)
    }
  }
  return (
    <div className="rounded-lg border border-linesoft bg-surface2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-2.5">
        <span className="font-book text-[15px] font-bold">{user.username}</span>
        <Tag kind={isAdminUser ? "brand" : "dim"}>{user.role}</Tag>
        {personal && <TagPill tag={personal} on={false} size="sm" />}
        {isAdminUser ? (
          <span className="text-[12px] text-faint">sees every book</span>
        ) : shared.length === 0 ? (
          <span className="text-[12px] text-faint">only what they add themselves</span>
        ) : (
          shared.map(id => byId.get(id)).filter(Boolean).map(t => <TagPill key={t!.id} tag={t!} on size="sm" />)
        )}
        <span className="ml-auto" />
        {!isAdminUser && (
          <Button variant="outline" className="h-8" onClick={() => {
            setSelected(shared)
            setEditing(e => !e)
          }}>
            {editing ? "Cancel" : "Tags"}
          </Button>
        )}
        <Button variant="outline" className="h-8 text-faint" title="Switch off this account's OPDS password — a lost e-reader, say"
          onClick={opdsOff}>
          OPDS off
        </Button>
        <Button variant="outline" size="icon" className="h-8 w-8 text-faint" aria-label={`Delete ${user.username}`}
          onClick={onDelete}>
          <X className="h-3.5 w-3.5" />
        </Button>
      </div>
      {editing && (
        <div className="mt-3 border-t border-linesoft pt-3">
          <Label>Books shared with these tags reach {user.username}</Label>
          <TagPicker tags={tags} value={selected} onChange={setSelected} hidePersonal />
          <Button className="mt-3 h-9" onClick={save}>Save</Button>
        </div>
      )}
    </div>
  )
}

export function UsersPanel() {
  const { data, loading, reload } = useApi(() => delivery.users())
  const { tags, reload: reloadTags } = useTags()
  const users = data?.users ?? []
  const [adding, setAdding] = useState(false)
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [role, setRole] = useState<"admin" | "user">("user")
  const [selected, setSelected] = useState<number[]>([])

  // the first account is forced to admin, and admins see every book, so the
  // picker only matters once we're actually creating a plain user
  const effectiveRole = users.length === 0 ? "admin" : role
  const needsTags = effectiveRole === "user"

  const create = async () => {
    try {
      await delivery.createUser(username.trim(), password, effectiveRole, needsTags ? selected : [])
      toast.success(`User "${username.trim()}" created`)
      setAdding(false); setUsername(""); setPassword(""); setSelected([])
      reload(); reloadTags()
    } catch (e) {
      toast.error(`Couldn't create: ${e instanceof Error ? e.message : e}`)
    }
  }
  const remove = async (id: number, name: string) => {
    try {
      await delivery.deleteUser(id)
      toast(`User "${name}" deleted`)
      reload(); reloadTags()
    } catch (e) {
      toast.error(`Couldn't delete: ${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <Card title="Users"
      action={
        <Button variant="outline" size="icon" className="h-7 w-7 rounded-full" aria-label="Add user" onClick={() => setAdding(a => !a)}>
          <Plus className="h-4 w-4" />
        </Button>
      }
      desc="Web UI accounts. Until the first user exists Booky is open; creating one turns login on (the first account is always an admin). A 'user' sees the books shared with their tags plus whatever they add for themselves (their personal tag): they can browse, search and add books, refresh metadata and grab releases, but not edit metadata, delete files, change tags, or reach these settings. Each person sets their own OPDS password under Account; KoReader devices carry their own token.">
      {loading && <p className="mono-label text-faint">loading…</p>}
      {!loading && users.length === 0 && !adding && (
        <p className="text-[13px] text-muted-foreground">No accounts — anyone who can reach this page has full access. Add an admin to require login.</p>
      )}
      <div className="flex flex-col gap-2">
        {users.map(u => (
          <UserRow key={u.id} user={u} tags={tags}
            onDelete={() => remove(u.id, u.username)} onChanged={reload} />
        ))}
        {adding && (
          <div className="rounded-lg border border-dashed border-linesoft px-4 py-3">
            <div className="flex flex-wrap items-end gap-3">
              <div>
                <Label>Username</Label>
                <Input value={username} onChange={e => setUsername(e.target.value)} className="h-9 w-[140px]" />
              </div>
              <div>
                <Label>Password (min 8 chars)</Label>
                <Input type="password" value={password} onChange={e => setPassword(e.target.value)} className="h-9 w-[180px]" />
              </div>
              <div>
                <Label>Role</Label>
                <select className="h-9 rounded-md border border-input bg-transparent px-2.5 text-[12.5px]"
                  value={effectiveRole} disabled={users.length === 0}
                  onChange={e => setRole(e.target.value as "admin" | "user")}>
                  <option value="admin">admin</option>
                  <option value="user">user</option>
                </select>
              </div>
            </div>
            {needsTags && (
              <div className="mt-3">
                <Label>Shared tags</Label>
                <TagPicker tags={tags} value={selected} onChange={setSelected} hidePersonal
                  empty="No shared tags yet — they'll start with only what they add themselves. Create tags under Settings → Tags." />
                <p className="mt-1.5 text-[12px] text-faint">
                  They see books carrying any of these, plus everything they add themselves. Change it any time.
                </p>
              </div>
            )}
            <div className="mt-3 flex gap-2">
              <Button className="h-9" disabled={!username.trim() || password.length < 8} onClick={create}>Create</Button>
              <Button variant="outline" className="h-9" onClick={() => setAdding(false)}>Cancel</Button>
            </div>
          </div>
        )}
      </div>
    </Card>
  )
}

/* ---------- Backups ---------- */

export function BackupsPanel() {
  const { data, loading, reload } = useApi(() => delivery.backups())
  const backups = data?.backups ?? []
  const [busy, setBusy] = useState(false)

  const create = async () => {
    setBusy(true)
    try {
      const r = await delivery.createBackup()
      toast.success(`Backup written: ${r.name}`)
      reload()
    } catch (e) {
      toast.error(`Backup failed: ${e instanceof Error ? e.message : e}`)
    } finally {
      setBusy(false)
    }
  }
  const restore = async (name: string) => {
    if (!window.confirm(`Restore ${name}?\n\nCurrent state is replaced by the backup and Booky restarts.`)) return
    try {
      await delivery.restoreBackup(name)
      toast("Restoring — Booky is restarting…")
    } catch (e) {
      toast.error(`Restore failed: ${e instanceof Error ? e.message : e}`)
    }
  }

  const mb = (b: number) => (b / 1024 / 1024).toFixed(1) + " MB"

  return (
    <>
      <Card title="Backups"
        action={<Button variant="outline" className="h-8" disabled={busy} onClick={create}>{busy ? "Backing up…" : "Back up now"}</Button>}
        desc="Zips of the database into /config/backups. A scheduled backup runs weekly and old archives are pruned; restoring swaps the database and restarts.">
        {loading && <p className="mono-label text-faint">loading…</p>}
        {!loading && backups.length === 0 && <p className="text-[13px] text-muted-foreground">No backups yet.</p>}
        <div className="flex flex-col">
          {backups.map(b => (
            <div key={b.name} className="flex items-center gap-3 border-b border-linesoft py-2.5 last:border-b-0">
              <span className="font-label text-[12px]">{b.name}</span>
              <span className="text-xs text-faint">{mb(b.sizeBytes)}</span>
              <span className="ml-auto" />
              <Button variant="outline" className="h-8" onClick={() => restore(b.name)}>Restore</Button>
            </div>
          ))}
        </div>
      </Card>
      <Card title="Schedule" desc="The weekly backup runs in the background; turn it off if you snapshot /config some other way.">
        <div className="grid max-w-md gap-5">
          <ScheduleToggle />
          <SettingField settingKey="backup_keep" label="Archives to keep" placeholder="4" />
        </div>
      </Card>
    </>
  )
}

// ScheduleToggle flips the weekly backup on/off.
function ScheduleToggle() {
  const [enabled, setEnabled] = useState<boolean | null>(null)
  useEffect(() => {
    api.getSetting("backup_enabled").then(r => setEnabled(r.value !== "false")).catch(() => setEnabled(true))
  }, [])
  const flip = async (v: boolean) => {
    setEnabled(v)
    try {
      await api.putSetting("backup_enabled", v ? "true" : "false")
      toast.success(v ? "Weekly backups on" : "Weekly backups off")
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  return (
    <label className="flex w-fit cursor-pointer items-center gap-3 text-[13.5px] font-medium">
      <Switch checked={enabled ?? true} disabled={enabled === null} onCheckedChange={flip} />
      Weekly scheduled backup
    </label>
  )
}

/* ---------- KoReader devices ---------- */

// DeviceTags is the shared editor for a device's tags: which it carries, and
// which of those it auto-downloads. `allowed` caps the choice to what the
// pairing account holds — the server enforces the same against the owner.
function DeviceTags({ tags, allowed, sel, setSel, auto, setAuto, keepPersonal }: {
  tags: ApiTag[]
  allowed?: number[]
  sel: number[]
  setSel: (v: number[]) => void
  auto: number[]
  setAuto: (v: number[]) => void
  keepPersonal: number
}) {
  return (
    <>
      <div className="mt-3">
        <Label>Books tagged</Label>
        <TagPicker tags={tags} allowed={allowed} hidePersonal keepPersonal={keepPersonal} value={sel}
          onChange={next => { setSel(next); setAuto(auto.filter(id => next.includes(id))) }} />
      </div>
      <div className="mt-3">
        <Label>Auto-download new arrivals tagged</Label>
        <TagPicker tags={tags} allowed={sel} value={auto.filter(id => sel.includes(id))} onChange={setAuto}
          empty="Pick the device's tags above first." />
      </div>
    </>
  )
}

function DeviceRow({ device, tags, allowed, keepPersonal, onChanged }: {
  device: ApiDevice
  tags: ApiTag[]
  allowed?: number[]
  keepPersonal: number
  onChanged: () => void
}) {
  const byId = new Map(tags.map(t => [t.id, t]))
  // in-place editing: the token survives, so the device keeps syncing against
  // the new tags immediately — only the zip's own baked lists go stale
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState(device.name)
  const [sel, setSel] = useState<number[]>(device.tagIds)
  const [auto, setAuto] = useState<number[]>(device.autoTagIds)
  const startEdit = () => {
    setName(device.name)
    setSel(device.tagIds); setAuto(device.autoTagIds)
    setEditing(true)
  }
  const save = async () => {
    try {
      await delivery.updateDevice(device.id, name.trim(), sel, auto.filter(id => sel.includes(id)))
      toast.success(`"${name.trim()}" updated — syncing follows the new tags right away`)
      setEditing(false)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  const revoke = async () => {
    if (!window.confirm(`Revoke "${device.name}"? Its plugin stops syncing immediately.`)) return
    try {
      await delivery.revokeDevice(device.id)
      toast(`Device "${device.name}" revoked`)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't revoke: ${e instanceof Error ? e.message : e}`)
    }
  }
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border border-linesoft bg-surface2 px-4 py-3">
      <div className="min-w-[140px]">
        <div className="font-book text-[15px] font-bold">{device.name}</div>
        <div className="text-[11.5px] text-faint" title={formatFull(device.lastSync)}>
          {device.lastSync ? `last sync ${formatWhen(device.lastSync)}` : "never synced"}
        </div>
      </div>
      {/* only admins are sent an owner, and only they see other people's
          devices — so this labels exactly the rows that need it */}
      {device.ownerName && <Tag kind="dim">{device.ownerName}</Tag>}
      {device.tagIds.map(id => {
        const t = byId.get(id)
        const isAuto = device.autoTagIds.includes(id)
        return (
          <Tag key={id} kind={isAuto ? "info" : "dim"}>
            {t?.name ?? `#${id}`}{isAuto ? " · auto" : ""}
          </Tag>
        )
      })}
      <div className="ml-auto flex gap-2">
        <Button variant="outline" className="h-8" asChild>
          <a href={delivery.pluginZipUrl(device.id)} download>
            <Download className="mr-1.5 h-3.5 w-3.5" /> Plugin zip
          </a>
        </Button>
        <Button variant="outline" size="icon" className="h-8 w-8" aria-label={`Edit ${device.name}`}
          onClick={() => (editing ? setEditing(false) : startEdit())}>
          <Pencil className="h-3.5 w-3.5" />
        </Button>
        <Button variant="outline" size="icon" className="h-8 w-8 text-faint" aria-label={`Revoke ${device.name}`} onClick={revoke}>
          <X className="h-3.5 w-3.5" />
        </Button>
      </div>
      {editing && (
        <div className="w-full rounded-lg border border-dashed border-linesoft px-4 py-3">
          <div>
            <Label>Device name</Label>
            <Input value={name} onChange={e => setName(e.target.value)} className="h-9 w-[180px]" />
          </div>
          <DeviceTags tags={tags} allowed={allowed} sel={sel} setSel={setSel} auto={auto} setAuto={setAuto}
            keepPersonal={keepPersonal} />
          <p className="mt-2 text-xs text-faint">Syncing follows the new tags immediately — the token doesn't change. Download a fresh plugin zip when convenient so the device's own list matches.</p>
          <div className="mt-3 flex gap-2">
            <Button className="h-9" disabled={!name.trim() || sel.length === 0} onClick={save}>Save</Button>
            <Button variant="outline" className="h-9" onClick={() => setEditing(false)}>Cancel</Button>
          </div>
        </div>
      )}
    </div>
  )
}

export function DevicesPanel() {
  const { isAdmin, tagIds, personalTagId } = useAccess()
  const { data, loading, reload } = useApi(() => delivery.devices())
  // already scoped by the server: a user is offered only their own tags
  const { tags } = useTags()
  // an admin may pick any tag; everyone else what they hold
  const allowed = isAdmin ? undefined : tagIds
  // polled so saving the URL in the card below unblocks Add device live
  const { data: urlData } = useApi(() => api.getSetting("server_url"), 5_000)
  const serverUrlMissing = urlData !== undefined && (urlData?.value ?? "").trim() === ""
  const devices = data?.devices ?? []
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState("")
  const [selected, setSelected] = useState<number[]>([])
  const [auto, setAuto] = useState<number[]>([])

  const create = async () => {
    try {
      await delivery.createDevice(name.trim(), selected, auto.filter(id => selected.includes(id)))
      toast.success(`Device "${name.trim()}" added — download its plugin zip`)
      setAdding(false); setName(""); setSelected([]); setAuto([])
      reload()
    } catch (e) {
      toast.error(`Couldn't add device: ${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <>
      <Card title="KoReader Devices"
        action={
          <Button variant="outline" size="icon" className="h-7 w-7 rounded-full" aria-label="Add device" onClick={() => setAdding(a => !a)}>
            <Plus className="h-4 w-4" />
          </Button>
        }
        desc={
          isAdmin
            ? "Each device gets its own preconfigured plugin zip: extract it, copy the resulting booky.koplugin folder into KOReader's plugins directory, and it syncs over wifi — no credentials to type on the device, the zip carries its own token and the server address. A device carries books with the tags you pick (never more than its owner holds, checked on every sync); auto-download tags push new arrivals. Revoking a device cuts it off instantly. You see every account's devices and can revoke any of them."
            : "Pair your own e-reader: each device gets its own preconfigured plugin zip — extract it, copy the resulting booky.koplugin folder into KOReader's plugins directory, and it syncs over wifi with no credentials to type. Pick which of your tags it carries; only your own devices appear here."
        }>
        {serverUrlMissing && (
          <div className="mb-3 rounded-r-lg border-l-[3px] border-want bg-want/10 px-4 py-2.5 text-[13px]">
            {isAdmin
              ? "Set the Server URL below first — it's baked into every plugin zip, so devices created without it can't sync."
              : "Booky's server URL isn't set yet — ask an admin to fill it in before pairing a device."}
          </div>
        )}
        {loading && <p className="mono-label text-faint">loading…</p>}
        {!loading && devices.length === 0 && !adding && (
          <p className="text-[13px] text-muted-foreground">
            {isAdmin ? "No devices yet." : "You haven't paired a device yet."}
          </p>
        )}
        <div className="flex flex-col gap-2">
          {devices.map(d => (
            <DeviceRow key={d.id} device={d} tags={tags} allowed={allowed} keepPersonal={personalTagId} onChanged={reload} />
          ))}
          {adding && (
            <div className="rounded-lg border border-dashed border-linesoft px-4 py-3">
              <div className="flex flex-wrap items-end gap-3">
                <div>
                  <Label>Device name</Label>
                  <Input value={name} onChange={e => setName(e.target.value)} className="h-9 w-[180px]" placeholder="My Kobo" />
                </div>
              </div>
              <DeviceTags tags={tags} allowed={allowed} sel={selected} setSel={setSelected} auto={auto} setAuto={setAuto}
                keepPersonal={personalTagId} />
              <div className="mt-3 flex gap-2">
                <Button className="h-9" disabled={!name.trim() || selected.length === 0 || serverUrlMissing} onClick={create}>Add device</Button>
                <Button variant="outline" className="h-9" onClick={() => setAdding(false)}>Cancel</Button>
              </div>
            </div>
          )}
        </div>
      </Card>
      {isAdmin && (
        <Card title="Server URL" desc="Baked into every plugin zip so devices know where to sync — and shown as the OPDS address under Account. Use an address your e-readers can reach on your network.">
          <div className="max-w-md">
            <SettingField settingKey="server_url" label="Server URL" placeholder="http://192.168.1.10:8787" />
          </div>
        </Card>
      )}
    </>
  )
}
