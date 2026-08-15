import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Logo } from "@/components/Logo"
import { delivery } from "@/api"

// LoginView gates the whole app once any account exists. OPDS readers and
// KoReader devices never see this — they authenticate on their own routes.
export function LoginView({ onLoggedIn }: { onLoggedIn: () => void }) {
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await delivery.login(username.trim(), password)
      onLoggedIn()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background">
      <form onSubmit={submit} className="w-[340px] rounded-lg border bg-surface p-8">
        <div className="mb-6 flex justify-center"><Logo /></div>
        <div className="mb-4">
          <div className="mono-label mb-1.5 text-muted-foreground">Username</div>
          <Input autoFocus value={username} onChange={e => setUsername(e.target.value)} className="h-10" />
        </div>
        <div className="mb-5">
          <div className="mono-label mb-1.5 text-muted-foreground">Password</div>
          <Input type="password" value={password} onChange={e => setPassword(e.target.value)} className="h-10" />
        </div>
        {error && <p className="mb-4 text-[12.5px] text-want">{error}</p>}
        <Button type="submit" className="h-10 w-full" disabled={busy || !username.trim() || !password}>
          {busy ? "Signing in…" : "Sign in"}
        </Button>
      </form>
    </div>
  )
}
