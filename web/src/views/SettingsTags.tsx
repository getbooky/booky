import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { tags as tagsApi, delivery } from "@/api"
import type { ApiTag, ApiUser } from "@/api"
import { TagPill, useTags } from "@/components/TagPills"
import { useApi } from "@/hooks/use-api"
import { toast } from "sonner"
import { Check, Pencil, Plus, Trash2, User, X } from "lucide-react"
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

// MemberPills toggles accounts in and out of a tag. Admins see every book
// whatever they hold, so they're listed but there's nothing to grant them.
function MemberPills({ users, value, onToggle, busy }: {
  users: ApiUser[]
  value: number[]
  onToggle: (id: number) => void
  busy?: boolean
}) {
  if (users.length === 0) return <p className="text-[12.5px] text-faint">No accounts yet.</p>
  return (
    <div className="flex flex-wrap gap-1.5">
      {users.map(u => {
        const on = value.includes(u.id)
        const admin = u.role === "admin"
        return (
          <button key={u.id} type="button" disabled={busy || admin} aria-pressed={on}
            title={admin ? "Admins see every book already" : undefined}
            onClick={() => onToggle(u.id)}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[12px] font-medium transition-colors",
              on ? "border-brass/50 bg-brass/15 text-brass" : "border-linesoft bg-surface2/60 text-muted-foreground",
              !admin && !on && "hover:border-brass/40 hover:text-foreground",
              admin && "opacity-50",
            )}>
            {on ? <Check className="h-3 w-3" /> : <User className="h-3 w-3" />}
            {u.username}{admin ? " · sees all" : ""}
          </button>
        )
      })}
    </div>
  )
}

function TagRow({ tag, users, onChanged }: { tag: ApiTag; users: ApiUser[]; onChanged: () => void }) {
  const [renaming, setRenaming] = useState(false)
  const [name, setName] = useState(tag.name)
  const [busy, setBusy] = useState(false)
  const members = tag.memberIds ?? []

  const toggle = async (userId: number) => {
    const next = members.includes(userId) ? members.filter(id => id !== userId) : [...members, userId]
    setBusy(true)
    try {
      await tagsApi.setMembers(tag.id, next)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't update: ${e instanceof Error ? e.message : e}`)
    } finally {
      setBusy(false)
    }
  }
  const rename = async () => {
    try {
      await tagsApi.rename(tag.id, name.trim())
      toast.success(`Renamed to "${name.trim()}"`)
      setRenaming(false)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't rename: ${e instanceof Error ? e.message : e}`)
    }
  }
  const remove = async () => {
    if (!window.confirm(
      `Delete the tag "${tag.name}"?\n\n` +
      `Its ${tag.bookCount} book${tag.bookCount === 1 ? "" : "s"} stop reaching its members; any book only this tag shared becomes admin-only. ` +
      "Nothing on disk is touched.")) return
    try {
      await tagsApi.remove(tag.id)
      toast(`Tag "${tag.name}" deleted`)
      onChanged()
    } catch (e) {
      toast.error(`Couldn't delete: ${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <div className="rounded-lg border border-linesoft bg-surface2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        {renaming ? (
          <span className="flex items-center gap-2">
            <Input value={name} onChange={e => setName(e.target.value)} className="h-8 w-[180px]"
              onKeyDown={e => { if (e.key === "Enter" && name.trim()) void rename() }} />
            <Button className="h-8" disabled={!name.trim()} onClick={rename}>Save</Button>
            <Button variant="outline" className="h-8" onClick={() => { setRenaming(false); setName(tag.name) }}>Cancel</Button>
          </span>
        ) : (
          <TagPill tag={tag} on />
        )}
        <span className="text-xs text-muted-foreground">{tag.bookCount} book{tag.bookCount === 1 ? "" : "s"}</span>
        <div className="ml-auto flex gap-2">
          {!renaming && (
            <Button variant="outline" size="icon" className="h-8 w-8" aria-label={`Rename ${tag.name}`}
              onClick={() => setRenaming(true)}>
              <Pencil className="h-3.5 w-3.5" />
            </Button>
          )}
          <Button variant="outline" size="icon" aria-label={`Delete ${tag.name}`}
            className="h-8 w-8 text-muted-foreground hover:border-want hover:text-want" onClick={remove}>
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>
      <div className="mt-3">
        <Label>Who holds it</Label>
        <MemberPills users={users} value={members} onToggle={toggle} busy={busy} />
      </div>
    </div>
  )
}

export function TagsPanel() {
  const { tags, reload, loading } = useTags()
  const usersQuery = useApi(() => delivery.users())
  const users = usersQuery.data?.users ?? []
  const shared = tags.filter(t => !t.personal)
  const personal = tags.filter(t => t.personal)
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState("")
  const [members, setMembers] = useState<number[]>([])

  const create = async () => {
    try {
      await tagsApi.create(name.trim(), members)
      toast.success(`Tag "${name.trim()}" created`)
      setAdding(false); setName(""); setMembers([])
      reload()
    } catch (e) {
      toast.error(`Couldn't create: ${e instanceof Error ? e.message : e}`)
    }
  }
  const changed = () => { reload(); usersQuery.reload() }

  return (
    <>
      <Card title="Tags"
        action={
          <Button variant="outline" size="icon" className="h-7 w-7 rounded-full" aria-label="Add tag" onClick={() => setAdding(a => !a)}>
            <Plus className="h-4 w-4" />
          </Button>
        }
        desc="Tags decide who sees what. Every book lives once in the one library; a person sees a book when they hold one of its tags — on the web, over OPDS, on their KoReader and on their Kindle. Admins see everything, and a book with no tags is admin-only. Tag books from a book's page or several at once from the library.">
        {loading && <p className="mono-label text-faint">loading…</p>}
        {!loading && shared.length === 0 && !adding && (
          <p className="text-[13px] text-muted-foreground">No shared tags yet — make one like "Household" or "Kids" and add the people it's for.</p>
        )}
        <div className="flex flex-col gap-2">
          {shared.map(t => <TagRow key={t.id} tag={t} users={users} onChanged={changed} />)}
          {adding && (
            <div className="rounded-lg border border-dashed border-linesoft px-4 py-3">
              <Label>Name</Label>
              <Input value={name} onChange={e => setName(e.target.value)} className="h-9 w-[200px]" placeholder="Kids"
                onKeyDown={e => { if (e.key === "Enter" && name.trim()) void create() }} />
              <div className="mt-3">
                <Label>Who holds it</Label>
                <MemberPills users={users} value={members}
                  onToggle={id => setMembers(m => m.includes(id) ? m.filter(x => x !== id) : [...m, id])} />
              </div>
              <div className="mt-3 flex gap-2">
                <Button className="h-9" disabled={!name.trim()} onClick={create}>Create</Button>
                <Button variant="outline" className="h-9" onClick={() => setAdding(false)}>
                  <X className="mr-1 h-3.5 w-3.5" /> Cancel
                </Button>
              </div>
            </div>
          )}
        </div>
      </Card>

      <Card title="Personal tags"
        desc="Each account's own — what they add for themselves lands here. They come and go with the account and can't be granted to anyone else.">
        {personal.length === 0 && <p className="text-[13px] text-muted-foreground">No accounts yet.</p>}
        <div className="flex flex-wrap gap-2">
          {personal.map(t => (
            <span key={t.id} className="flex items-center gap-2">
              <TagPill tag={t} on={false} />
              <span className="text-xs text-faint">{t.bookCount}</span>
            </span>
          ))}
        </div>
      </Card>
    </>
  )
}
