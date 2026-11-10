import { useEffect, useMemo, useRef, useState } from "react"
import { Toaster, toast } from "sonner"
import { Search, Settings as SettingsIcon } from "lucide-react"
import { cn } from "@/lib/utils"
import { api } from "@/api"
import type { ApiAuthor, ApiBook, ApiLibrary, ApiSeries, ApiTag, ApiUser } from "@/api"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel,
  DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useApi } from "@/hooks/use-api"
import { Sidebar, BottomNav } from "@/components/Sidebar"
import type { BrowseView, CustomScope } from "@/components/Sidebar"
import { AddScopeDialog } from "@/components/AddScopeDialog"
import { LibraryView } from "@/views/Library"
import { HomeView } from "@/views/Home"
import type { Scope, ScopeFilter } from "@/views/Library"
import { SeriesView } from "@/views/Series"
import { SeriesDetailView } from "@/views/SeriesDetail"
import { AuthorView } from "@/views/Author"
import { AuthorsIndexView } from "@/views/AuthorsIndex"
import { CalendarView } from "@/views/Calendar"
import { WantedView } from "@/views/Wanted"
import { ActivityView } from "@/views/Activity"
import { SettingsView, ReviewScreen } from "@/views/Settings"
import { AddBookDialog } from "@/components/AddBookDialog"
import { useTags } from "@/components/TagPills"
import { BookDetail } from "@/components/BookDetail"
import { LoginView } from "@/views/Login"
import { InviteView, inviteTokenFromHash } from "@/views/Invite"
import { ReaderView } from "@/views/Reader"
import { SetupWizard } from "@/components/SetupWizard"
import { AccessProvider, accessFor } from "@/lib/access"
import { delivery, SESSION_ENDED } from "@/api"
import { Logo } from "@/components/Logo"
import { Button } from "@/components/ui/button"

type View = BrowseView | "settings"

// Everything that defines "where you are" lives in one snapshot so the
// browser's back/forward buttons work: each navigation pushes a history
// entry carrying the snapshot, popstate restores it, and because
// history.state survives reloads, a refresh keeps your place too.
type Nav = {
  view: View
  scope: Scope
  library: ApiLibrary | null
  /** the library filtered to one tag's books (sidebar "Shared with") */
  tag: ApiTag | null
  customScope: CustomScope | null
  book: ApiBook | null
  author: ApiAuthor | null
  series: ApiSeries | null
  focusSeries?: string
  review: ApiLibrary | null
  reader: ApiBook | null
  /** the reader plays the audiobook along with the text */
  readAlong?: boolean
}

const HOME: Nav = { view: "home", scope: "all", library: null, tag: null, customScope: null, book: null, author: null, series: null, review: null, reader: null }

const routeFor = (n: Nav): string => {
  if (n.reader) return `#/${n.readAlong ? "listen" : "read"}/${n.reader.id}`
  if (n.review) return `#/review/${n.review.id}`
  if (n.book) return `#/book/${n.book.id}`
  if (n.view === "library") {
    if (n.customScope) return `#/scope/${encodeURIComponent(n.customScope.name)}`
    if (n.tag) return `#/tag/${n.tag.id}`
    if (n.library) return `#/library/${n.library.id}`
    if (n.scope !== "all") return `#/${n.scope}`
    return "#/library"
  }
  if (n.view === "author") return n.author ? `#/author/${n.author.id}` : "#/authors"
  if (n.view === "series") return n.series ? `#/series/${n.series.id}` : "#/series"
  return `#/${n.view}`
}

// A pasted or bookmarked deep link can't carry the full snapshot, so it
// lands on the nearest top-level view (only reloads restore exact spots,
// via history.state).
const navFromHash = (): Nav => {
  const head = window.location.hash.replace(/^#\/?/, "").split("/")[0]
  switch (head) {
    case "library": return { ...HOME, view: "library" }
    case "series": return { ...HOME, view: "series" }
    case "author": case "authors": return { ...HOME, view: "author" }
    case "calendar": case "wanted": case "activity": return { ...HOME, view: head as View }
    case "settings": return { ...HOME, view: "settings" }
    case "recent": case "missing": case "upgrade": return { ...HOME, scope: head as Scope }
    default: return HOME
  }
}

// App boots behind an auth probe: once any account exists the API demands a
// session, so unauthenticated visitors get the login screen instead of a
// wall of failed requests.
export default function App() {
  const me = useApi(() => delivery.me())
  // a request answered 401 mid-session: the session ended (it ran out, or
  // the password was changed elsewhere) — ask again, which lands on sign-in
  const reloadMe = me.reload
  // an invite link ("#/invite/<token>") takes over the screen until it's
  // used or left, signed in or not
  const [invite, setInvite] = useState(() => inviteTokenFromHash(window.location.hash))
  useEffect(() => {
    const onHash = () => setInvite(inviteTokenFromHash(window.location.hash))
    window.addEventListener("hashchange", onHash)
    return () => window.removeEventListener("hashchange", onHash)
  }, [])
  const leaveInvite = () => {
    // drop the token from the address so it isn't left in the history
    window.history.replaceState(null, "", window.location.pathname + window.location.search)
    setInvite(null)
  }
  useEffect(() => {
    const onEnded = () => reloadMe()
    window.addEventListener(SESSION_ENDED, onEnded)
    return () => window.removeEventListener(SESSION_ENDED, onEnded)
  }, [reloadMe])
  if (me.loading && !me.data) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background" aria-busy="true" aria-label="Loading Booky">
        <div className="animate-pulse opacity-70"><Logo /></div>
      </div>
    )
  }
  // No answer about who's signed in: say so. Carrying on would show the
  // app as if nobody needed to sign in — setup wizard, admin controls and
  // all — with every request behind it failing.
  if (!me.data) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background px-4">
        <div className="w-full max-w-[360px] rounded-lg border bg-surface p-8 text-center">
          <div className="mb-5 flex justify-center"><Logo /></div>
          <p className="mb-1 text-[14px] font-medium">Can't reach Booky</p>
          <p className="mb-5 text-[12.5px] text-muted-foreground">{me.error ?? "The server didn't answer."}</p>
          <Button className="h-9" onClick={() => me.reload()}>Try again</Button>
        </div>
      </div>
    )
  }
  if (invite) {
    return (
      <>
        <Toaster position="bottom-left" expand />
        <InviteView token={invite} signedInAs={me.data.user?.username ?? null}
          onJoined={() => { leaveInvite(); me.reload() }}
          onLeave={leaveInvite}
          onSignedOut={me.reload} />
      </>
    )
  }
  if (me.data?.authRequired && !me.data.user) {
    return (
      <>
        <Toaster position="bottom-left" expand />
        <LoginView onLoggedIn={me.reload} />
      </>
    )
  }
  return <BookyApp user={me.data?.user ?? null} usersExist={me.data?.authRequired === true} onLoggedOut={me.reload} />
}

function BookyApp({ user, usersExist, onLoggedOut }: { user: ApiUser | null; usersExist: boolean; onLoggedOut: () => void }) {
  // Everything below renders against the signed-in account's role: admins get
  // the management controls, a plain user gets their libraries and nothing
  // that would come back 403.
  const access = useMemo(() => accessFor(user), [user])
  const [nav, setNav] = useState<Nav>(() => (window.history.state?.nav as Nav) ?? navFromHash())
  const [addOpen, setAddOpen] = useState(false)
  const [refreshTick, setRefreshTick] = useState(0)
  const [addScopeOpen, setAddScopeOpen] = useState(false)

  useEffect(() => {
    // A link someone pasted or bookmarked — "#/book/12" — names a thing
    // but carries no snapshot: look it up and go there. (Without this a
    // shared link to a book opened Home.)
    const m = /^#\/(book|read|listen|author|series)\/(\d+)/.exec(window.location.hash)
    const fresh = !window.history.state?.nav
    if (m && fresh) {
      const id = Number(m[2])
      const land = (next: Nav) => {
        setNav(next)
        window.history.replaceState({ nav: next }, "", routeFor(next))
      }
      const lookup = async () => {
        if (m[1] === "author") {
          const author = (await api.authors()).authors?.find(a => a.id === id)
          if (author) land({ ...HOME, view: "author", author })
        } else if (m[1] === "series") {
          const series = (await api.series()).series?.find(sr => sr.id === id)
          if (series) land({ ...HOME, view: "series", series })
        } else {
          const book = await api.book(id)
          if (m[1] === "book") land({ ...HOME, view: "library", book })
          else land({ ...HOME, view: "library", reader: book, readAlong: m[1] === "listen" })
        }
      }
      // a link to something gone, or not shared with this account, stays
      // where navFromHash put it
      lookup().catch(() => {})
    }
    if (fresh && !m) {
      window.history.replaceState({ nav }, "", routeFor(nav))
    }
    const onPop = (e: PopStateEvent) => {
      setNav((e.state?.nav as Nav) ?? navFromHash())
      window.scrollTo(0, 0)
    }
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // the tab (and the phone's app switcher, and history) says where you are
  useEffect(() => {
    const names: Record<string, string> = {
      home: "Home", library: "Library", series: "Series", authors: "Authors",
      calendar: "Calendar", wanted: "Wanted", activity: "Activity", settings: "Settings",
    }
    const where = nav.reader?.title ?? nav.book?.title ?? nav.author?.name ?? nav.series?.name
      ?? nav.tag?.name ?? names[nav.view] ?? ""
    document.title = where ? `${where} · Booky` : "Booky"
  }, [nav])

  // Every navigation goes through here: a fresh snapshot (unspecified fields
  // reset to HOME) pushed as a history entry, so back always retraces steps.
  const navRef = useRef(nav)
  navRef.current = nav
  const go = (next: Partial<Nav>, opts?: { replace?: boolean }) => {
    const full: Nav = { ...HOME, ...next }
    if (JSON.stringify(full) === JSON.stringify(navRef.current)) return
    setNav(full)
    if (opts?.replace) window.history.replaceState({ nav: full }, "", routeFor(full))
    else window.history.pushState({ nav: full }, "", routeFor(full))
    window.scrollTo(0, 0)
  }
  const back = () => window.history.back()
  // the reader overlays whatever you're looking at; back returns you there
  const openReader = (b: ApiBook) => go({ ...navRef.current, reader: b, readAlong: false })
  const openReadAlong = (b: ApiBook) => go({ ...navRef.current, reader: b, readAlong: true })

  const scopesQuery = useApi(async () => {
    try {
      const r = await api.getSetting("user_scopes")
      const scopes = (r.value ? JSON.parse(r.value) : []) as CustomScope[]
      // scopes saved before filters had operators default to "is"
      return scopes.map(s => ({ ...s, filters: s.filters.map(f => ({ op: "is", ...f })) }))
    } catch { return [] as CustomScope[] }
  })
  const customScopes = scopesQuery.data ?? []
  const saveScopes = async (scopes: CustomScope[]) => {
    await api.putSetting("user_scopes", JSON.stringify(scopes))
    scopesQuery.reload()
  }

  // The sidebar shows book counts and the needs-attention badge, which change
  // when anyone imports — not just when this session does something. bumped()
  // covers our own actions; the interval covers everyone else's.
  const libs = useApi(() => api.libraries(), 10_000)
  const libraries = libs.data?.libraries ?? []
  const tagsQuery = useTags()
  const bumped = () => { setRefreshTick(t => t + 1); libs.reload(); tagsQuery.reload() }

  // First run: the wizard greets exactly while NO account exists — its first
  // step (creating the admin) is the one mandatory piece of setup, and once
  // any account is made it never auto-opens again, libraries or not. An
  // explicit dismissal is remembered per-browser so it doesn't nag every
  // load; admins can re-run it any time from Settings → About.
  const [wizardOpen, setWizardOpen] = useState(false)
  const wizardOffered = useRef(false)
  useEffect(() => {
    if (wizardOffered.current) return
    if (!usersExist && !localStorage.getItem("booky-setup-offered")) {
      wizardOffered.current = true
      setWizardOpen(true)
    }
  }, [usersExist])
  const closeWizard = (v: boolean) => {
    setWizardOpen(v)
    if (!v) { localStorage.setItem("booky-setup-offered", "1"); bumped() }
  }

  const scan = async (l: ApiLibrary) => {
    toast(`Scanning ${l.name}…`)
    try {
      const r = await api.scanLibrary(l.id)
      toast.success(`${l.name}: ${r.scanned} files · ${r.matched} matched · ${r.review} need review`)
      bumped()
    } catch (e) {
      toast.error(`Scan failed: ${e instanceof Error ? e.message : e}`)
    }
  }
  const refresh = async (l: ApiLibrary) => {
    try {
      await api.refreshLibrary(l.id)
      toast.success(`${l.name}: metadata refresh started — locked fields stay untouched`)
    } catch (e) {
      toast.error(`${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <AccessProvider value={access}>
    <div className="flex min-h-screen">
      <Sidebar
        view={nav.view} activeTagId={nav.tag?.id ?? null} scope={nav.scope}
        library={libraries[0] ?? null} tags={tagsQuery.tags} personalTagId={access.personalTagId}
        customScopes={customScopes} activeCustomScope={nav.customScope?.name ?? null}
        onAddScope={() => setAddScopeOpen(true)}
        onSelectCustomScope={cs => go({ view: "library", customScope: cs })}
        onDeleteCustomScope={async name => {
          await saveScopes(customScopes.filter(c => c.name !== name))
          if (navRef.current.customScope?.name === name) go({ view: "library" }, { replace: true })
          toast(`Scope "${name}" deleted`)
        }}
        onBrowse={v => go({ view: v })}
        onSelectTag={t => go({ view: "library", tag: t })}
        onSelectScope={s => go({ view: "library", scope: s })}
        canManage={access.isAdmin}
        onScan={scan}
        onRefresh={refresh}
        onReview={l => go({ view: "library", review: l })}
      />

      {/* main */}
      <div className="min-w-0 flex-1">
        {/* floating top bar */}
        <div className="sticky top-[calc(env(safe-area-inset-top)+0.75rem)] z-30 mx-3 mt-[calc(env(safe-area-inset-top)+0.75rem)] flex items-center gap-2 rounded-2xl border border-linesoft bg-surface px-3 py-2 shadow-[0_8px_28px_rgba(0,0,0,0.25)] md:mx-5 md:gap-3">
          <button
            onClick={() => setAddOpen(true)}
            className="flex w-full max-w-[440px] items-center gap-2.5 rounded-[10px] border border-linesoft bg-background px-3 py-2 text-left text-[13px] text-faint hover:border-brass/60"
          >
            <Search className="h-3.5 w-3.5" />
            Search books, authors, series…
          </button>
          <div className="flex-1" />
          <button
            aria-label="Settings" title="Settings"
            onClick={() => go({ view: "settings" })}
            className={cn(
              "flex h-9 w-9 items-center justify-center rounded-[10px] text-muted-foreground hover:bg-surface2 hover:text-foreground",
              nav.view === "settings" && "bg-brass/15 text-brass"
            )}
          >
            <SettingsIcon className="h-[18px] w-[18px]" />
          </button>
          {/* account menu — only rendered when someone is actually signed in */}
          {user && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button aria-label={`Account: ${user.username}`} title={user.username}
                  className="font-book flex h-8 w-8 items-center justify-center rounded-full bg-brass text-xs font-bold uppercase text-brass-ink hover:brightness-110">
                  {user.username.slice(0, 1)}
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-[180px] rounded-xl">
                <DropdownMenuLabel className="mono-label text-faint">
                  {user.username} · {user.role}
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={async () => {
                  try {
                    await delivery.logout()
                    onLoggedOut()
                  } catch (e) { toast.error(`Logout failed: ${e instanceof Error ? e.message : e}`) }
                }}>
                  Log out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>

        <main className="w-full px-4 pb-28 pt-5 md:px-7 md:pb-20 md:pt-7">
          {nav.review ? (
            <ReviewScreen library={nav.review} onBack={() => { back(); bumped() }} />
          ) : (
            <>
              {nav.view === "library" && (nav.book
                ? <BookDetail key={nav.book.id} book={nav.book} onBack={back} onChanged={bumped}
                    onRead={openReader} onReadAlong={openReadAlong}
                    onOpenSeries={async b => {
                      if (!b.seriesId) return
                      const series = ((await api.series()).series ?? []).find(s => s.id === b.seriesId)
                      if (series) go({ view: "series", series })
                    }}
                    onOpenAuthor={async b => {
                      const author = ((await api.authors()).authors ?? []).find(a => a.id === b.authorId)
                      if (author) go({ view: "author", author })
                    }} />
                : <LibraryView key={`${refreshTick}-${nav.tag?.id ?? 0}-${nav.scope}-${nav.customScope?.name ?? ""}`}
                    library={null} tag={nav.tag} scope={nav.scope} libraries={libraries}
                    initialFilters={nav.customScope?.filters as ScopeFilter[] | undefined}
                    scopeTitle={nav.customScope?.name}
                    onOpenBook={b => go({ view: "library", book: b })}
                    onOpenAuthor={async b => {
                      const author = ((await api.authors()).authors ?? []).find(a => a.id === b.authorId)
                      if (author) go({ view: "author", author })
                    }}
                    onOpenSeries={async b => {
                      if (!b.seriesId) return
                      const series = ((await api.series()).series ?? []).find(s => s.id === b.seriesId)
                      if (series) go({ view: "series", series })
                    }}
                    onRead={openReader}
                    onSelectTag={t => go({ view: "library", tag: t })} />)}
              {nav.view === "home" && (
                <HomeView key={refreshTick} libraries={libraries} userName={user?.username}
                  onOpenBook={b => go({ view: "library", book: b })}
                  onOpenTag={t => go({ view: "library", tag: t })}
                  onBrowse={v => go({ view: v })} />
              )}
              {nav.view === "series" && (nav.series
                ? <SeriesDetailView key={nav.series.id} series={nav.series}
                    onBack={back}
                    onOpenBook={b => go({ view: "library", book: b })}
                    onOpenAuthor={async () => {
                      const authors = (await api.authors()).authors ?? []
                      const author = authors.find(a => a.id === navRef.current.series?.authorId)
                      if (author) go({ view: "author", author })
                    }} />
                : <SeriesView key={refreshTick}
                    onOpenSeries={s => go({ view: "series", series: s })}
                    onOpenAuthor={async s => {
                      const author = ((await api.authors()).authors ?? []).find(a => a.id === s.authorId)
                      if (author) go({ view: "author", author })
                    }} />)}
              {nav.view === "author" && (nav.author
                ? <AuthorView key={nav.author.id + (nav.focusSeries ?? "")} author={nav.author} focusSeries={nav.focusSeries}
                    onBack={back} onRead={openReader}
                    onOpenBook={b => go({ view: "library", book: b })} />
                : <AuthorsIndexView key={refreshTick} onOpenAuthor={a => go({ view: "author", author: a })} />)}
              {nav.view === "calendar" && <CalendarView libraries={libraries}
                onOpenBook={b => go({ view: "library", book: b })} />}
              {nav.view === "wanted" && <WantedView libraries={libraries} />}
              {nav.view === "activity" && <ActivityView />}
              {nav.view === "settings" && <SettingsView />}
            </>
          )}
        </main>
      </div>

      <BottomNav view={nav.view} onBrowse={v => go({ view: v })} />

      {nav.reader && <ReaderView key={`${nav.reader.id}-${nav.readAlong ? 1 : 0}`} book={nav.reader} readAlong={!!nav.readAlong} onClose={back} />}

      <AddBookDialog open={addOpen} onOpenChange={setAddOpen} libraries={libraries} onAdded={bumped} />
      <SetupWizard open={wizardOpen} onOpenChange={closeWizard} onDone={bumped} />
      <AddScopeDialog open={addScopeOpen} onOpenChange={setAddScopeOpen}
        onSave={async (name, filters) => {
          await saveScopes([...customScopes.filter(c => c.name !== name), { name, filters }])
          toast.success(`Scope "${name}" saved`)
        }} />
      {/* bottom-left, under the sidebar's empty tail. expand keeps them a
          real stack — sonner's default piles them as overlapping cards that
          only fan out on hover, so a burst of toasts reads as one smudge. */}
      <Toaster position="bottom-left" expand mobileOffset={{ bottom: 76 }} toastOptions={{
        style: { background: "hsl(var(--surface2))", color: "hsl(var(--foreground))", border: "1px solid hsl(var(--border))", borderRadius: "12px" },
      }} />
    </div>
    </AccessProvider>
  )
}
