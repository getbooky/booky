import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tag } from "@/components/bits"
import { api, acquisition, watchers } from "@/api"
import type { ApiWatchedList, ListPayload } from "@/api"
import { useApi } from "@/hooks/use-api"
import { toast } from "sonner"
import { Plus, RefreshCw, X } from "lucide-react"
import { cn } from "@/lib/utils"

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

const selectCls = "h-9 rounded-md border border-input bg-transparent px-2.5 text-[12.5px]"

const SCOPES = [
  { v: "book", label: "Listed Book" },
  { v: "series", label: "Whole Series" },
  { v: "author", label: "Author Backlist" },
]
const REMOVES = [
  { v: "nothing", label: "Do nothing (default)" },
  { v: "unmonitor", label: "Unmonitor the book" },
  { v: "delete", label: "Remove from library (deletes this library's file)" },
]

function AddListForm({ kind, onDone, onCancel }: { kind: "goodreads_rss" | "hardcover"; onDone: () => void; onCancel: () => void }) {
  const { data: libData } = useApi(() => api.libraries())
  const { data: profData } = useApi(() => acquisition.profiles())
  const libraries = libData?.libraries ?? []
  const profiles = profData?.profiles ?? []

  const [sourceRef, setSourceRef] = useState("")
  const [hcUser, setHcUser] = useState("")
  const [libraryId, setLibraryId] = useState(0)
  const [scope, setScope] = useState("book")
  const [onRemove, setOnRemove] = useState("nothing")
  const [searchOnAdd, setSearchOnAdd] = useState(true)
  const [profileId, setProfileId] = useState(0)
  const [saving, setSaving] = useState(false)

  // discovery: fetch the account's shelves / lists, pick which to watch
  const [shelves, setShelves] = useState<{ name: string; count: number }[] | null>(null)
  const [hcLists, setHcLists] = useState<{ id: string; name: string; count: number }[] | null>(null)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [finding, setFinding] = useState(false)

  const togglePick = (key: string) => {
    setPicked(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const findShelves = async () => {
    setFinding(true)
    try {
      const r = await acquisition.discoverGoodreads(sourceRef.trim())
      setShelves(r.shelves)
      setPicked(new Set(["to-read"]))
    } catch (e) {
      toast.error(`Couldn't load shelves: ${e instanceof Error ? e.message : e}`)
    } finally {
      setFinding(false)
    }
  }

  // no user typed → the token owner's lists; a @handle or URL → their public lists
  const findHcLists = async () => {
    setFinding(true)
    try {
      const r = await acquisition.discoverHardcover(hcUser.trim() || undefined)
      setHcLists(r.lists)
      setPicked(new Set())
      if (r.lists.length === 0) toast(hcUser.trim() ? "No public lists on that account" : "No lists on this Hardcover account yet")
    } catch (e) {
      toast.error(`Couldn't load lists (is the token set under Settings → Metadata?): ${e instanceof Error ? e.message : e}`)
    } finally {
      setFinding(false)
    }
  }

  // one watched list per picked shelf / hardcover list
  const create = async () => {
    setSaving(true)
    try {
      const common = {
        libraryId: libraryId || libraries[0]?.id || 0,
        monitorScope: scope, onRemove, searchOnAdd, enabled: true,
        qualityProfileId: profileId || undefined,
      }
      let made = 0
      if (kind === "goodreads_rss") {
        for (const shelf of picked) {
          await watchers.createList({
            ...common, kind, sourceRef: sourceRef.trim(), shelf,
            name: `Goodreads ${shelf}`,
          } as ListPayload)
          made++
        }
      } else {
        for (const id of picked) {
          const list = hcLists?.find(l => l.id === id)
          const who = hcUser.trim() ? `${hcUser.trim().replace(/^@/, "")} · ` : ""
          await watchers.createList({
            ...common, kind, sourceRef: id, name: list ? `${who}${list.name}` : `Hardcover ${id}`,
          } as ListPayload)
          made++
        }
      }
      toast.success(`Watching ${made} list${made === 1 ? "" : "s"}`)
      onDone()
    } catch (e) {
      toast.error(`Couldn't add: ${e instanceof Error ? e.message : e}`)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="rounded-lg border border-dashed border-linesoft p-4">
      {kind === "goodreads_rss" ? (
        <>
          <div className="flex flex-wrap items-end gap-2">
            <div className="min-w-[260px] flex-1">
              <Label>Goodreads profile URL or user ID</Label>
              <Input value={sourceRef} onChange={e => setSourceRef(e.target.value)} className="h-9 text-[12.5px]"
                placeholder="https://www.goodreads.com/user/show/12345678" />
            </div>
            <Button variant="outline" className="h-9" disabled={finding || !sourceRef.trim()} onClick={findShelves}>
              {finding ? "Looking…" : "Find shelves"}
            </Button>
          </div>
          <p className="mt-1.5 text-xs text-faint">The profile must be public. Booky reads shelf RSS feeds — no login needed.</p>
          {shelves && (
            <div className="mt-3">
              <Label>Watch these shelves</Label>
              <div className="flex flex-wrap gap-x-5 gap-y-1.5">
                {shelves.map(s => (
                  <label key={s.name} className="flex cursor-pointer items-center gap-2 text-[13px]">
                    <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]"
                      checked={picked.has(s.name)} onChange={() => togglePick(s.name)} />
                    {s.name}
                    {s.count >= 0 && <span className="text-xs text-faint">{s.count} books</span>}
                  </label>
                ))}
              </div>
            </div>
          )}
        </>
      ) : (
        <>
          <div className="flex flex-wrap items-end gap-2">
            <div className="min-w-[260px] flex-1">
              <Label>Whose lists? Leave blank for your own</Label>
              <Input value={hcUser} onChange={e => setHcUser(e.target.value)} className="h-9 text-[12.5px]"
                placeholder="@username or hardcover.app profile URL — blank = my lists" />
            </div>
            <Button variant="outline" className="h-9" disabled={finding} onClick={findHcLists}>
              {finding ? "Looking…" : hcUser.trim() ? "Find their lists" : "Load my lists"}
            </Button>
          </div>
          <p className="mt-1.5 text-xs text-faint">Uses your Hardcover API token from Settings → Metadata. Anyone's public lists can be watched — paste their profile URL or @username.</p>
          {hcLists && hcLists.length > 0 && (
            <div className="mt-3">
              <Label>Watch these lists</Label>
              <div className="flex flex-col gap-1.5">
                {hcLists.map(l => (
                  <label key={l.id} className="flex cursor-pointer items-center gap-2 text-[13px]">
                    <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]"
                      checked={picked.has(l.id)} onChange={() => togglePick(l.id)} />
                    <span className="font-medium">{l.name}</span>
                    <span className="text-xs text-faint">{l.count} books</span>
                  </label>
                ))}
              </div>
            </div>
          )}
        </>
      )}
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <div>
          <Label>Into library</Label>
          <select className={selectCls} value={libraryId || libraries[0]?.id || 0} onChange={e => setLibraryId(Number(e.target.value))}>
            {libraries.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}
          </select>
        </div>
        <div>
          <Label>Monitor</Label>
          <select className={selectCls} value={scope} onChange={e => setScope(e.target.value)}>
            {SCOPES.map(s => <option key={s.v} value={s.v}>{s.label}</option>)}
          </select>
        </div>
        <div>
          <Label>When a book leaves the list</Label>
          <select className={selectCls} value={onRemove} onChange={e => setOnRemove(e.target.value)}>
            {REMOVES.map(s => <option key={s.v} value={s.v}>{s.label}</option>)}
          </select>
        </div>
        <div>
          <Label>Quality profile</Label>
          <select className={selectCls} value={profileId} onChange={e => setProfileId(Number(e.target.value))}>
            <option value={0}>Library default</option>
            {profiles.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        </div>
      </div>
      <div className="mt-3 flex items-center gap-4">
        <label className="flex cursor-pointer items-center gap-2 text-[13px]">
          <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]" checked={searchOnAdd}
            onChange={e => setSearchOnAdd(e.target.checked)} />
          Search when a book is added
        </label>
        <div className="ml-auto flex gap-2">
          <Button className="h-9" disabled={saving || picked.size === 0 || libraries.length === 0} onClick={create}>
            {saving ? "Adding…" : picked.size > 1 ? `Watch ${picked.size} lists` : "Watch list"}
          </Button>
          <Button variant="outline" className="h-9" onClick={onCancel}>Cancel</Button>
        </div>
      </div>
      {libraries.length === 0 && <p className="mt-2 text-xs text-want">Create a library first — lists route their books into one.</p>}
    </div>
  )
}

function ListRow({ list, onChanged }: { list: ApiWatchedList; onChanged: () => void }) {
  const [polling, setPolling] = useState(false)
  const scopeLabel = SCOPES.find(s => s.v === list.monitorScope)?.label ?? list.monitorScope

  const poll = async () => {
    setPolling(true)
    try {
      const r = await watchers.pollList(list.id)
      toast.success(r.added > 0 ? `${list.name}: ${r.added} new book${r.added === 1 ? "" : "s"}` : `${list.name}: nothing new`)
      onChanged()
    } catch (e) {
      toast.error(`Check failed: ${e instanceof Error ? e.message : e}`)
      onChanged() // last_error is now set on the row
    } finally {
      setPolling(false)
    }
  }
  const toggle = async () => {
    try {
      await watchers.updateList(list.id, { enabled: !list.enabled })
      onChanged()
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  const remove = async () => {
    try {
      await watchers.deleteList(list.id)
      toast(`Stopped watching "${list.name}" — its books stay in the library`)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't delete: ${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <div className={cn("rounded-lg border border-linesoft bg-surface2 px-4 py-3", !list.enabled && "opacity-60")}>
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-[160px]">
          <div className="font-book text-[16px] font-bold">{list.name}</div>
          <div className="text-[11.5px] text-faint">
            {list.kind === "goodreads_rss" ? `goodreads · ${list.sourceRef}` : `hardcover · list ${list.sourceRef}`}
          </div>
        </div>
        <Tag kind="brand">{list.itemCount} book{list.itemCount === 1 ? "" : "s"}</Tag>
        <Tag kind="dim">→ {list.libraryName}</Tag>
        <Tag kind="dim">{scopeLabel}</Tag>
        {list.searchOnAdd && <Tag kind="info">search on add</Tag>}
        <div className="ml-auto flex items-center gap-2">
          <label className="flex cursor-pointer items-center gap-1.5 text-[12px] text-muted-foreground">
            <input type="checkbox" className="h-4 w-4 accent-[hsl(var(--brass))]" checked={list.enabled} onChange={toggle} />
            enabled
          </label>
          <Button variant="outline" className="h-8" disabled={polling} onClick={poll}>
            <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", polling && "animate-spin")} /> Check now
          </Button>
          <Button variant="outline" size="icon" className="h-8 w-8 text-faint" aria-label={`Delete ${list.name}`} onClick={remove}>
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>
      <div className="mt-1.5 text-[11.5px]">
        {list.lastError
          ? <span className="text-want">last check failed: {list.lastError}</span>
          : <span className="text-faint">{list.lastChecked ? `last checked ${list.lastChecked} UTC` : "not checked yet"}</span>}
      </div>
    </div>
  )
}

function PollSettings() {
  const [interval, setIntervalV] = useState<string | null>(null)
  useEffect(() => {
    api.getSetting("list_poll_seconds").then(r => setIntervalV(r.value)).catch(() => setIntervalV("60"))
  }, [])

  const saveInterval = async () => {
    const n = Math.max(30, Number(interval) || 60)
    setIntervalV(String(n))
    try {
      await api.putSetting("list_poll_seconds", String(n))
      toast.success(`Poll interval saved (${n}s)`)
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    }
  }
  return (
    <>
      <Card title="List Polling"
        desc="How often the lists above are re-checked for new books. Each list is checked on its own staggered schedule, so several lists never hit Goodreads or Hardcover at the same moment.">
        <Label>Poll interval (seconds, min 30)</Label>
        <div className="flex items-center gap-2">
          <Input value={interval ?? ""} disabled={interval === null} onChange={e => setIntervalV(e.target.value)}
            className="h-9 w-[100px] text-[12.5px]" inputMode="numeric" />
          <Button variant="outline" className="h-9" onClick={saveInterval}>Save</Button>
        </div>
      </Card>

    </>
  )
}

function ListSection({ title, desc, kind, lists, loading, empty, onChanged }: {
  title: string
  desc: string
  kind: "goodreads_rss" | "hardcover"
  lists: ApiWatchedList[]
  loading: boolean
  empty: string
  onChanged: () => void
}) {
  const [adding, setAdding] = useState(false)
  return (
    <Card title={title}
      action={
        <Button variant="outline" size="icon" className="h-7 w-7 rounded-full" aria-label={`Watch a ${title} list`} onClick={() => setAdding(a => !a)}>
          <Plus className="h-4 w-4" />
        </Button>
      }
      desc={desc}>
      <div className="flex flex-col gap-2">
        {loading && <p className="mono-label text-faint">loading…</p>}
        {!loading && lists.length === 0 && !adding && (
          <p className="text-[13px] text-muted-foreground">{empty}</p>
        )}
        {lists.map(l => <ListRow key={l.id} list={l} onChanged={onChanged} />)}
        {adding && <AddListForm kind={kind} onDone={() => { setAdding(false); onChanged() }} onCancel={() => setAdding(false)} />}
      </div>
    </Card>
  )
}

export function ListsPanel() {
  const { data, loading, reload } = useApi(() => watchers.lists())
  const lists = data?.lists ?? []

  return (
    <>
      <ListSection title="Goodreads Shelves" kind="goodreads_rss"
        desc="Public shelves polled through their RSS feeds — no Goodreads login needed. New books route into a library the moment they appear."
        lists={lists.filter(l => l.kind === "goodreads_rss")} loading={loading}
        empty="No shelves watched yet — add a public Goodreads profile."
        onChanged={reload} />
      <ListSection title="Hardcover Lists" kind="hardcover"
        desc="Your own lists, or anyone's public lists. Removing a book from a watched list never deletes files unless you explicitly choose that behavior."
        lists={lists.filter(l => l.kind !== "goodreads_rss")} loading={loading}
        empty="No lists watched yet — load your own or paste another user's profile."
        onChanged={reload} />
      <PollSettings />
    </>
  )
}
