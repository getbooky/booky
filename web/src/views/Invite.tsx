import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Logo } from "@/components/Logo"
import { delivery, invites } from "@/api"

/** The token in an invite link's address ("#/invite/<token>"), if it is one. */
export function inviteTokenFromHash(hash: string): string | null {
  const m = /^#\/invite\/([A-Za-z0-9_-]{16,200})$/.exec(hash)
  return m ? m[1] : null
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background py-8">
      <div className="mx-4 w-full max-w-[360px] rounded-lg border bg-surface p-6 sm:p-8">
        <div className="mb-6 flex justify-center"><Logo /></div>
        {children}
      </div>
    </div>
  )
}

// InviteView is where an invite link lands: the person it was sent to picks
// their own username and password, and is signed in. Someone already signed
// in is told so rather than shown a sign-up form.
export function InviteView({ token, signedInAs, onJoined, onLeave, onSignedOut }: {
  token: string
  signedInAs: string | null
  onJoined: () => void
  onLeave: () => void
  onSignedOut: () => void
}) {
  const [state, setState] = useState<"checking" | "ok" | "bad">("checking")
  const [problem, setProblem] = useState("")
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [again, setAgain] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let live = true
    invites.check(token).then(
      () => { if (live) setState("ok") },
      e => { if (live) { setProblem(e instanceof Error ? e.message : String(e)); setState("bad") } },
    )
    return () => { live = false }
  }, [token])

  if (signedInAs) {
    const signOut = async () => {
      try { await delivery.logout() } catch { /* the session may already be gone */ }
      onSignedOut()
    }
    return (
      <Shell>
        <h1 className="mb-2 text-center text-[15px] font-medium">You're already signed in</h1>
        <p className="mb-5 text-center text-[13px] text-muted-foreground">
          This invite link creates a new account, and you're signed in as <span className="font-medium text-foreground">{signedInAs}</span>.
        </p>
        <Button className="mb-2 h-10 w-full" onClick={onLeave}>Go to Booky</Button>
        <Button variant="outline" className="h-10 w-full" onClick={signOut}>Sign out and use the link</Button>
      </Shell>
    )
  }

  if (state === "checking") {
    return <Shell><p className="mono-label text-center text-faint" aria-busy="true">checking your invite…</p></Shell>
  }
  if (state === "bad") {
    return (
      <Shell>
        <h1 className="mb-2 text-center text-[15px] font-medium">This invite can't be used</h1>
        <p role="alert" className="mb-5 text-center text-[13px] text-muted-foreground">{problem}{/[.!?]$/.test(problem) ? "" : "."}</p>
        <Button variant="outline" className="h-10 w-full" onClick={onLeave}>Go to sign in</Button>
      </Shell>
    )
  }

  const tooShort = password.length > 0 && password.length < 8
  const mismatch = again.length > 0 && again !== password
  const ready = username.trim() !== "" && password.length >= 8 && again === password

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!ready) return
    setBusy(true)
    setError(null)
    try {
      await invites.accept(token, username.trim(), password)
      onJoined()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <Shell>
      <form onSubmit={submit}>
        <h1 className="mb-1 text-center text-[15px] font-medium">You're invited</h1>
        <p className="mb-5 text-center text-[13px] text-muted-foreground">Choose a username and password to create your account.</p>
        <div className="mb-4">
          <label htmlFor="invite-username" className="mono-label mb-1.5 block text-muted-foreground">Username</label>
          <Input id="invite-username" name="username" autoFocus value={username} onChange={e => setUsername(e.target.value)}
            autoComplete="username" autoCapitalize="none" autoCorrect="off" spellCheck={false} maxLength={64} className="h-10" />
        </div>
        <div className="mb-4">
          <label htmlFor="invite-password" className="mono-label mb-1.5 block text-muted-foreground">Password</label>
          <Input id="invite-password" name="new-password" type="password" value={password} onChange={e => setPassword(e.target.value)}
            autoComplete="new-password" maxLength={72} aria-describedby="invite-password-hint" className="h-10" />
          <p id="invite-password-hint" className={`mt-1.5 text-[12px] ${tooShort ? "text-want" : "text-faint"}`}>At least 8 characters.</p>
        </div>
        <div className="mb-5">
          <label htmlFor="invite-password-again" className="mono-label mb-1.5 block text-muted-foreground">Password again</label>
          <Input id="invite-password-again" name="new-password-again" type="password" value={again} onChange={e => setAgain(e.target.value)}
            autoComplete="new-password" maxLength={72} className="h-10" />
          {mismatch && <p className="mt-1.5 text-[12px] text-want">The passwords don't match.</p>}
        </div>
        {error && <p role="alert" className="mb-4 text-[12.5px] text-want">{error}</p>}
        <Button type="submit" className="h-10 w-full" disabled={busy || !ready}>
          {busy ? "Creating your account…" : "Create account"}
        </Button>
      </form>
    </Shell>
  )
}
