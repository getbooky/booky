import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Tag } from "@/components/bits"
import { api, delivery } from "@/api"
import type { ApiDevice, ApiLibrary, ApiUser } from "@/api"
import { useAccess } from "@/lib/access"
import { formatFull, formatWhen } from "@/lib/time"
import { useApi } from "@/hooks/use-api"
import { toast } from "sonner"
import { Download, Plus, X } from "lucide-react"
import { SettingField } from "@/views/Settings"

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

// LibraryPicker is the checkbox list shared by the create form and the
// per-user editor below.
function LibraryPicker({ libraries, selected, onToggle }: {
  libraries: ApiLibrary[]
  selected: Set<number>
  onToggle: (id: number) => void
}) {
  if (libraries.length === 0) {
    return <p className="text-[12.5px] text-faint">No libraries yet — create one first.</p>
  }
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1.5">
      {libraries.map(l => (
        <label key={l.id} className="flex cursor-pointer items-center gap-2 text-[13px]">
          <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]"
            checked={selected.has(l.id)} onChange={() => onToggle(l.id)} />
          <span className="font-medium">{l.name}</span>
        </label>
      ))}
    </div>
  )
}

// UserRow shows a user's libraries and lets an admin re-scope them without
// deleting the account. Admins have no list — they reach everything.
function UserRow({ user, libraries, onDelete, onChanged }: {
  user: ApiUser
  libraries: ApiLibrary[]
  onDelete: () => void
  onChanged: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [selected, setSelected] = useState<Set<number>>(new Set(user.libraryIds ?? []))
  const isAdminUser = user.role === "admin"

  const toggle = (id: number) => {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setSelected(next)
  }
  const save = async () => {
    try {
      await delivery.setUserLibraries(user.id, [...selected])
      toast.success(`${user.username}'s libraries updated`)
      setEditing(false)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  return (
    <div className="rounded-lg border border-linesoft bg-surface2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="font-book text-[15px] font-bold">{user.username}</span>
        <Tag kind={isAdminUser ? "brand" : "dim"}>{user.role}</Tag>
        {isAdminUser ? (
          <span className="text-[12px] text-faint">every library</span>
        ) : (
          (user.libraryIds ?? []).length === 0
            ? <span className="text-[12px] text-want">no libraries assigned</span>
            : (user.libraryIds ?? []).map(id => (
                <Tag key={id} kind="dim">{libraries.find(l => l.id === id)?.name ?? `#${id}`}</Tag>
              ))
        )}
        <span className="ml-auto" />
        {!isAdminUser && (
          <Button variant="outline" className="h-8" onClick={() => {
            setSelected(new Set(user.libraryIds ?? []))
            setEditing(e => !e)
          }}>
            {editing ? "Cancel" : "Libraries"}
          </Button>
        )}
        <Button variant="outline" size="icon" className="h-8 w-8 text-faint" aria-label={`Delete ${user.username}`}
          onClick={onDelete}>
          <X className="h-3.5 w-3.5" />
        </Button>
      </div>
      {editing && (
        <div className="mt-3 border-t border-linesoft pt-3">
          <Label>Libraries this user may browse, add to and monitor</Label>
          <LibraryPicker libraries={libraries} selected={selected} onToggle={toggle} />
          <Button className="mt-3 h-9" onClick={save}>Save</Button>
        </div>
      )}
    </div>
  )
}

export function UsersPanel() {
  const { data, loading, reload } = useApi(() => delivery.users())
  const { data: libData } = useApi(() => api.libraries())
  const users = data?.users ?? []
  const libraries = libData?.libraries ?? []
  const [adding, setAdding] = useState(false)
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [role, setRole] = useState<"admin" | "user">("user")
  const [selected, setSelected] = useState<Set<number>>(new Set())

  // the first account is forced to admin, and admins reach every library, so
  // the picker only matters once we're actually creating a scoped user
  const effectiveRole = users.length === 0 ? "admin" : role
  const needsLibraries = effectiveRole === "user"

  const create = async () => {
    try {
      await delivery.createUser(username.trim(), password, effectiveRole, [...selected])
      toast.success(`User "${username.trim()}" created`)
      setAdding(false); setUsername(""); setPassword(""); setSelected(new Set())
      reload()
    } catch (e) {
      toast.error(`Couldn't create: ${e instanceof Error ? e.message : e}`)
    }
  }
  const remove = async (id: number, name: string) => {
    try {
      await delivery.deleteUser(id)
      toast(`User "${name}" deleted`)
      reload()
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
      desc="Web UI accounts. Until the first user exists Booky is open; creating one turns login on (the first account is always an admin). A 'user' works only in the libraries you assign: they can browse, search and add books, refresh metadata and monitor authors and series there, but not edit metadata, delete books, or reach any of these settings. OPDS feeds and KoReader devices use their own credentials — never these.">
      {loading && <p className="mono-label text-faint">loading…</p>}
      {!loading && users.length === 0 && !adding && (
        <p className="text-[13px] text-muted-foreground">No accounts — anyone who can reach this page has full access. Add an admin to require login.</p>
      )}
      <div className="flex flex-col gap-2">
        {users.map(u => (
          <UserRow key={u.id} user={u} libraries={libraries}
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
            {needsLibraries && (
              <div className="mt-3">
                <Label>Libraries this user may access</Label>
                <LibraryPicker libraries={libraries} selected={selected} onToggle={id => {
                  const next = new Set(selected)
                  if (next.has(id)) next.delete(id)
                  else next.add(id)
                  setSelected(next)
                }} />
                <p className="mt-1.5 text-[12px] text-faint">
                  They can add books to, and monitor authors and series for, exactly these — and see nothing else.
                </p>
              </div>
            )}
            <div className="mt-3 flex gap-2">
              <Button className="h-9"
                disabled={!username.trim() || password.length < 8 || (needsLibraries && selected.size === 0)}
                onClick={create}>Create</Button>
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

function DeviceRow({ device, libraries, onChanged }: { device: ApiDevice; libraries: ApiLibrary[]; onChanged: () => void }) {
  const libName = (id: number) => libraries.find(l => l.id === id)?.name ?? `#${id}`
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
      {device.libraryIds.map(id => (
        <Tag key={id} kind={device.autoLibraryIds.includes(id) ? "info" : "dim"}>
          {libName(id)}{device.autoLibraryIds.includes(id) ? " · auto" : ""}
        </Tag>
      ))}
      <div className="ml-auto flex gap-2">
        <Button variant="outline" className="h-8" asChild>
          <a href={delivery.pluginZipUrl(device.id)} download>
            <Download className="mr-1.5 h-3.5 w-3.5" /> Plugin zip
          </a>
        </Button>
        <Button variant="outline" size="icon" className="h-8 w-8 text-faint" aria-label={`Revoke ${device.name}`} onClick={revoke}>
          <X className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  )
}

export function DevicesPanel() {
  const { isAdmin } = useAccess()
  const { data, loading, reload } = useApi(() => delivery.devices())
  // already scoped by the server: a user is offered only their own libraries
  const { data: libData } = useApi(() => api.libraries())
  // polled so saving the URL in the card below unblocks Add device live
  const { data: urlData } = useApi(() => api.getSetting("server_url"), 5_000)
  const serverUrlMissing = urlData !== undefined && (urlData?.value ?? "").trim() === ""
  const devices = data?.devices ?? []
  const libraries = libData?.libraries ?? []
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState("")
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [auto, setAuto] = useState<Set<number>>(new Set())

  const toggle = (set: Set<number>, setter: (s: Set<number>) => void, id: number) => {
    const next = new Set(set)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setter(next)
  }
  const create = async () => {
    try {
      const libs = [...selected]
      await delivery.createDevice(name.trim(), libs, [...auto].filter(id => selected.has(id)))
      toast.success(`Device "${name.trim()}" added — download its plugin zip`)
      setAdding(false); setName(""); setSelected(new Set()); setAuto(new Set())
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
            ? "Each device gets its own preconfigured plugin zip: extract it, copy the resulting booky.koplugin folder into KOReader's plugins directory, and it syncs over wifi — no credentials to type on the device, the zip carries its own token and the server address. Auto-download libraries push new arrivals onto the device; revoking a device cuts it off instantly. You see every account's devices and can revoke any of them."
            : "Pair your own e-reader: each device gets its own preconfigured plugin zip — extract it, copy the resulting booky.koplugin folder into KOReader's plugins directory, and it syncs over wifi with no credentials to type. You can only pick from the libraries assigned to you, and only your own devices appear here."
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
          {devices.map(d => <DeviceRow key={d.id} device={d} libraries={libraries} onChanged={reload} />)}
          {adding && (
            <div className="rounded-lg border border-dashed border-linesoft px-4 py-3">
              <div className="flex flex-wrap items-end gap-3">
                <div>
                  <Label>Device name</Label>
                  <Input value={name} onChange={e => setName(e.target.value)} className="h-9 w-[180px]" placeholder="My Kobo" />
                </div>
              </div>
              <div className="mt-3">
                <Label>Libraries (check "auto" to push new arrivals)</Label>
                <div className="flex flex-col gap-1.5">
                  {libraries.map(l => (
                    <div key={l.id} className="flex items-center gap-4 text-[13px]">
                      <label className="flex w-[160px] cursor-pointer items-center gap-2">
                        <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]"
                          checked={selected.has(l.id)} onChange={() => toggle(selected, setSelected, l.id)} />
                        <span className="font-medium">{l.name}</span>
                      </label>
                      <label className="flex cursor-pointer items-center gap-1.5 text-[12px] text-muted-foreground">
                        <input type="checkbox" className="h-3.5 w-3.5 accent-[hsl(var(--brass))]" disabled={!selected.has(l.id)}
                          checked={auto.has(l.id) && selected.has(l.id)} onChange={() => toggle(auto, setAuto, l.id)} />
                        auto-download
                      </label>
                    </div>
                  ))}
                </div>
              </div>
              <div className="mt-3 flex gap-2">
                <Button className="h-9" disabled={!name.trim() || selected.size === 0 || serverUrlMissing} onClick={create}>Add device</Button>
                <Button variant="outline" className="h-9" onClick={() => setAdding(false)}>Cancel</Button>
              </div>
            </div>
          )}
        </div>
      </Card>
      {isAdmin && (
        <Card title="Server URL" desc="Baked into every plugin zip so devices know where to sync — use an address your e-readers can reach on your network.">
          <div className="max-w-md">
            <SettingField settingKey="server_url" label="Server URL" placeholder="http://192.168.1.10:8787" />
          </div>
        </Card>
      )}
    </>
  )
}
