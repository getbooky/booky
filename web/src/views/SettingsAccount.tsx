import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tag } from "@/components/bits"
import { api, delivery } from "@/api"
import { useApi } from "@/hooks/use-api"
import { toast } from "sonner"
import { Copy } from "lucide-react"

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

// AccountPanel is the signed-in person's own corner of Settings. For now
// that's their OPDS login: a reader app signs in with their username and a
// password separate from the web one, and sees exactly their tagged books.
export function AccountPanel() {
  const { data, loading, reload } = useApi(() => delivery.opds())
  const { data: urlData } = useApi(() => api.getSetting("server_url"))
  const [password, setPassword] = useState("")
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)

  const base = (urlData?.value ?? "").trim().replace(/\/$/, "") || window.location.origin
  const feedUrl = `${base}${data?.feedPath ?? "/opds"}`
  const configured = data?.configured === true

  const save = async (next: string) => {
    setBusy(true)
    try {
      await delivery.setOPDSPassword(next)
      toast.success(next ? "OPDS password saved — reader apps can sign in now" : "OPDS turned off")
      setPassword(""); setEditing(false)
      reload()
    } catch (e) {
      toast.error(`Couldn't save: ${e instanceof Error ? e.message : e}`)
    } finally {
      setBusy(false)
    }
  }
  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success("Copied")
    } catch {
      toast.error("Couldn't copy — select it instead")
    }
  }

  return (
    <Card title="OPDS"
      action={loading ? undefined : configured ? <Tag kind="good">on</Tag> : <Tag kind="dim">off</Tag>}
      desc="Read your books in any OPDS app (KOReader, Moon+ Reader, Thorium…). The feed shows exactly the books shared with you — your own and your tags' — and nothing else. It signs in with your username and an OPDS password that's separate from your web password: reader apps store it in plain text, so it can never be used to sign in here.">
      <div className="grid max-w-xl gap-4">
        <div>
          <Label>Feed address</Label>
          <div className="flex items-center gap-2">
            <code className="flex h-9 min-w-0 flex-1 items-center truncate rounded-md border border-input bg-surface2 px-3 font-label text-[12px]">{feedUrl}</code>
            <Button variant="outline" size="icon" className="h-9 w-9" aria-label="Copy feed address" onClick={() => copy(feedUrl)}>
              <Copy className="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>
        <div>
          <Label>Username</Label>
          <code className="flex h-9 items-center rounded-md border border-input bg-surface2 px-3 font-label text-[12px]">{data?.username ?? "…"}</code>
        </div>
        <div>
          <Label>OPDS password</Label>
          {configured && !editing ? (
            <div className="flex flex-wrap items-center gap-2">
              <div className="flex h-9 w-[220px] items-center rounded-md border border-input px-3 text-[13px] text-faint">
                <span className="tracking-widest">••••••••</span>
              </div>
              <Button variant="outline" className="h-9" onClick={() => setEditing(true)}>Change</Button>
              <Button variant="outline" className="h-9 text-faint" disabled={busy} onClick={() => {
                if (window.confirm("Turn off OPDS? Reader apps using it stop working until you set a new password.")) void save("")
              }}>Turn off</Button>
            </div>
          ) : (
            <div className="flex flex-wrap items-center gap-2">
              <Input type="password" value={password} onChange={e => setPassword(e.target.value)}
                placeholder="8+ characters" className="h-9 w-[220px]" autoComplete="new-password" />
              <Button className="h-9" disabled={busy || password.length < 8} onClick={() => save(password)}>
                {configured ? "Save new password" : "Turn on OPDS"}
              </Button>
              {editing && <Button variant="outline" className="h-9" onClick={() => { setEditing(false); setPassword("") }}>Cancel</Button>}
            </div>
          )}
          <p className="mt-1.5 text-xs text-faint">Changing it signs out every reader app using the old one. Five wrong tries lock the login for a short while.</p>
        </div>
      </div>
    </Card>
  )
}
