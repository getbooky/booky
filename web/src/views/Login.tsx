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
      <form onSubmit={submit} className="mx-4 w-full max-w-[340px] rounded-lg border bg-surface p-6 sm:p-8">
        <div className="mb-6 flex justify-center"><Logo /></div>
        <h1 className="sr-only">Sign in to Booky</h1>
        <div className="mb-4">
          <label htmlFor="login-username" className="mono-label mb-1.5 block text-muted-foreground">Username</label>
          <Input id="login-username" name="username" autoFocus value={username} onChange={e => setUsername(e.target.value)}
            autoComplete="username" autoCapitalize="none" autoCorrect="off" spellCheck={false} className="h-10" />
        </div>
        <div className="mb-5">
          <label htmlFor="login-password" className="mono-label mb-1.5 block text-muted-foreground">Password</label>
          <Input id="login-password" name="password" type="password" value={password} onChange={e => setPassword(e.target.value)}
            autoComplete="current-password" className="h-10" />
        </div>
        {error && <p role="alert" className="mb-4 text-[12.5px] text-want">{error}</p>}
        <Button type="submit" className="h-10 w-full" disabled={busy || !username.trim() || !password}>
          {busy ? "Signing in…" : "Sign in"}
        </Button>
      </form>
    </div>
  )
}
