import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
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
// that's where their OPDS feed lives: a reader app signs in with the same
// username and password as Booky itself and sees exactly their tagged books.
export function AccountPanel() {
  const { data } = useApi(() => delivery.opds())
  const { data: urlData } = useApi(() => api.getSetting("server_url"))

  const base = (urlData?.value ?? "").trim().replace(/\/$/, "") || window.location.origin
  const feedUrl = `${base}${data?.feedPath ?? "/opds"}`

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success("Copied")
    } catch {
      toast.error("Couldn't copy — select it instead")
    }
  }

  return (
    <>
    <PasswordCard username={data?.username ?? ""} />
    <Card title="OPDS"
      desc="Read your books in any OPDS app (KOReader, Moon+ Reader, Thorium…). Add this address and sign in with your usual Booky username and password. The feed shows exactly the books shared with you — your own and your tags' — and nothing else.">
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
          <p className="mt-1.5 text-xs text-faint">Password: the one you sign in to Booky with. Five wrong tries lock the login for a short while.</p>
        </div>
      </div>
    </Card>
    </>
  )
}

// PasswordCard lets the signed-in person change their own password. The
// hidden username field is for password managers: it tells them which
// saved login to update.
function PasswordCard({ username }: { username: string }) {
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [again, setAgain] = useState("")
  const [busy, setBusy] = useState(false)
  const mismatch = again !== "" && next !== again
  const tooShort = next !== "" && next.length < 8

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      await delivery.changePassword(current, next)
      toast.success("Password changed. Other devices signed in to your account have been signed out.")
      setCurrent(""); setNext(""); setAgain("")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card title="Password"
      desc="Change the password you sign in with. It's also what reader apps use for your OPDS feed, so update it there too.">
      <form onSubmit={submit} className="grid max-w-sm gap-4">
        <input type="text" name="username" autoComplete="username" value={username} readOnly hidden />
        <div>
          <label htmlFor="pw-current" className="mono-label mb-1.5 block text-muted-foreground">Current password</label>
          <Input id="pw-current" type="password" autoComplete="current-password" value={current}
            onChange={e => setCurrent(e.target.value)} className="h-9" />
        </div>
        <div>
          <label htmlFor="pw-new" className="mono-label mb-1.5 block text-muted-foreground">New password</label>
          <Input id="pw-new" type="password" autoComplete="new-password" value={next}
            onChange={e => setNext(e.target.value)} className="h-9" aria-invalid={tooShort} />
          <p className={tooShort ? "mt-1.5 text-xs text-want" : "mt-1.5 text-xs text-faint"}>At least 8 characters.</p>
        </div>
        <div>
          <label htmlFor="pw-again" className="mono-label mb-1.5 block text-muted-foreground">New password, again</label>
          <Input id="pw-again" type="password" autoComplete="new-password" value={again}
            onChange={e => setAgain(e.target.value)} className="h-9" aria-invalid={mismatch} />
          {mismatch && <p className="mt-1.5 text-xs text-want">The two don't match.</p>}
        </div>
        <div>
          <Button type="submit" className="h-9" disabled={busy || !current || next.length < 8 || next !== again}>
            {busy ? "Saving…" : "Change password"}
          </Button>
        </div>
      </form>
    </Card>
  )
}
