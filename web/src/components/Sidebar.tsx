import { useState } from "react"
import {
  Activity as ActivityIcon, Bookmark, CalendarDays, CheckCircle2, Clock, Heart, Layers,
  LibraryBig, MoreVertical, PanelLeftClose, PanelLeftOpen, Pencil,
  RefreshCw, Search, Trash2, TriangleAlert, Users, X,
} from "lucide-react"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Logo } from "@/components/Logo"
import type { ApiLibrary } from "@/api"
import type { Scope } from "@/views/Library"
import { cn } from "@/lib/utils"

export type BrowseView = "library" | "series" | "author" | "calendar" | "wanted" | "activity"

const BROWSE: { id: BrowseView; label: string; icon: React.ElementType; soon?: boolean }[] = [
  { id: "library", label: "Library", icon: LibraryBig },
  { id: "series", label: "Series", icon: Layers },
  { id: "author", label: "Authors", icon: Users },
  { id: "calendar", label: "Calendar", icon: CalendarDays },
  { id: "wanted", label: "Wanted", icon: Heart },
  { id: "activity", label: "Activity", icon: ActivityIcon },
]

const SCOPES: { id: Scope; label: string; icon: React.ElementType }[] = [
  { id: "recent", label: "Recently added", icon: Clock },
  { id: "missing", label: "Missing", icon: TriangleAlert },
  { id: "upgrade", label: "Upgrade wanted", icon: RefreshCw },
]

export const LIB_COLORS = ["#7da7d9", "#cf7268", "#86b06a", "#b08ad9", "#5fb0a5", "#d9a75f"]

function SectionHeader({ children, action, collapsed }: { children: React.ReactNode; action?: React.ReactNode; collapsed: boolean }) {
  if (collapsed) return <div className="mx-auto my-2 h-px w-6 bg-linesoft" />
  return (
    <div className="font-label flex items-center justify-between px-3 pb-1.5 pt-4 text-[10px] uppercase tracking-[0.13em] text-faint">
      {children}
      {action}
    </div>
  )
}

export interface CustomScope { name: string; filters: { field: string; value: string }[] }

// BottomNav is the phone-width replacement for the sidebar: the same browse
// destinations as fixed tabs along the bottom edge. Libraries and scopes
// live on their pages' own filters at this size.
export function BottomNav({ view, onBrowse }: {
  view: BrowseView | "settings"
  onBrowse: (v: BrowseView) => void
}) {
  return (
    <nav className="fixed inset-x-0 bottom-0 z-40 flex border-t border-linesoft bg-surface pb-[env(safe-area-inset-bottom)] md:hidden">
      {BROWSE.map(n => (
        <button key={n.id} onClick={() => onBrowse(n.id)} aria-label={n.label}
          className={cn(
            "flex min-w-0 flex-1 flex-col items-center gap-0.5 py-2 text-muted-foreground",
            view === n.id && "text-brass"
          )}>
          <n.icon className="h-[18px] w-[18px]" />
          <span className="font-label truncate text-[9px] uppercase tracking-[0.06em]">{n.label}</span>
        </button>
      ))}
    </nav>
  )
}

export function Sidebar({
  view, activeLibraryId, scope, libraries, customScopes, activeCustomScope,
  onBrowse, onSelectLibrary, onSelectScope, onAddLibrary, onScan, onRefresh, onReview, onEditLibrary,
  onDeleteLibrary, onAddScope, onSelectCustomScope, onDeleteCustomScope, canManage,
}: {
  view: BrowseView | "settings"
  activeLibraryId: number | null
  scope: Scope
  libraries: ApiLibrary[]
  customScopes: CustomScope[]
  activeCustomScope: string | null
  onBrowse: (v: BrowseView) => void
  onSelectLibrary: (l: ApiLibrary | null) => void
  onSelectScope: (s: Scope) => void
  onAddLibrary: () => void
  onScan: (l: ApiLibrary) => void
  onRefresh: (l: ApiLibrary) => void
  onReview: (l: ApiLibrary) => void
  onEditLibrary: (l: ApiLibrary) => void
  onDeleteLibrary: (l: ApiLibrary) => void
  onAddScope: () => void
  onSelectCustomScope: (s: CustomScope) => void
  onDeleteCustomScope: (name: string) => void
  /** Admin-only library management. A user still gets refresh on their own. */
  canManage: boolean
}) {
  const [collapsed, setCollapsed] = useState(false)
  const libraryActive = view === "library" && scope === "all"

  const item = (active: boolean) => cn(
    "flex w-full items-center gap-2.5 rounded-[10px] px-3 py-2 text-left text-[13.5px] font-medium text-muted-foreground transition-colors hover:bg-surface2 hover:text-foreground",
    collapsed && "justify-center px-0",
    active && "bg-brass/15 text-brass"
  )

  return (
    <aside className={cn(
      "sticky top-0 z-40 hidden h-screen shrink-0 flex-col overflow-y-auto border-r border-linesoft bg-surface px-2.5 pb-4 transition-[width] duration-150 motion-reduce:transition-none md:flex",
      collapsed ? "w-[62px]" : "w-[232px]"
    )}>
      <div className={cn("flex items-center pb-1 pt-4", collapsed ? "flex-col gap-2" : "justify-between pl-2.5 pr-1")}>
        <span className="flex items-center gap-2.5">
          <Logo size={collapsed ? 26 : 29} />
          {!collapsed && <span className="font-book text-[24px] font-bold italic leading-none">Book<span className="text-brass">y</span></span>}
        </span>
        <button
          onClick={() => setCollapsed(c => !c)}
          aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          className="rounded-lg p-1.5 text-faint hover:bg-surface2 hover:text-foreground"
        >
          {collapsed ? <PanelLeftOpen className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
        </button>
      </div>

      <SectionHeader collapsed={collapsed}>Browse</SectionHeader>
      {BROWSE.map(n => (
        <button key={n.id} title={collapsed ? n.label : undefined}
          onClick={() => onBrowse(n.id)}
          className={item(view === n.id && (n.id !== "library" || libraryActive || activeLibraryId === null))}>
          <n.icon className="h-4 w-4 shrink-0" />
          {!collapsed && n.label}
          {!collapsed && n.soon && (
            <span className="font-label ml-auto rounded-md border px-1 text-[8.5px] uppercase tracking-[0.06em] text-faint">soon</span>
          )}
        </button>
      ))}

      <SectionHeader collapsed={collapsed} action={canManage ? (
        <button onClick={onAddLibrary} aria-label="Add library"
          className="flex h-5 w-5 items-center justify-center rounded-full text-faint hover:bg-brass/15 hover:text-brass">
          <svg viewBox="0 0 24 24" width="13" height="13" style={{ stroke: "currentColor", fill: "none", strokeWidth: 2 }}><path d="M12 5v14M5 12h14" /></svg>
        </button>
      ) : undefined}>Libraries</SectionHeader>
      {libraries.map((l, i) => (
        <div key={l.id} className="group relative flex items-center">
          <button title={collapsed ? l.name : undefined}
            onClick={() => onSelectLibrary(l)}
            className={cn(item(view === "library" && activeLibraryId === l.id), !collapsed && "pr-8")}>
            <span className="h-2 w-2 shrink-0 rounded-[3px]" style={{ background: LIB_COLORS[i % LIB_COLORS.length] }} />
            {!collapsed && l.name}
            {!collapsed && l.reviewCount > 0 && (
              <span className="h-[7px] w-[7px] shrink-0 rounded-full bg-brass" title={`${l.reviewCount} imports need attention`} />
            )}
            {!collapsed && (
              <span className="font-label ml-auto rounded-lg bg-surface3 px-1.5 py-px text-[10px] text-muted-foreground">{l.bookCount}</span>
            )}
          </button>
          {!collapsed && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button aria-label={`${l.name} options`}
                  className="absolute right-1 flex h-6 w-6 items-center justify-center rounded-lg text-faint opacity-0 transition-opacity hover:bg-surface3 hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100 data-[state=open]:opacity-100">
                  <MoreVertical className="h-3.5 w-3.5" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent side="right" align="start" className="min-w-[200px] rounded-xl">
                {canManage && (
                  <DropdownMenuItem onClick={() => onScan(l)}>
                    <Search className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Scan library
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem onClick={() => onRefresh(l)}>
                  <RefreshCw className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Refresh metadata
                </DropdownMenuItem>
                {canManage && l.reviewCount > 0 && (
                  <DropdownMenuItem onClick={() => onReview(l)}>
                    <CheckCircle2 className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Import failed
                    <span className="font-label ml-auto rounded-lg bg-brass px-1.5 text-[9.5px] font-bold text-brass-ink">{l.reviewCount}</span>
                  </DropdownMenuItem>
                )}
                {canManage && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem onClick={() => onEditLibrary(l)}>
                      <Pencil className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Edit library
                    </DropdownMenuItem>
                    <DropdownMenuItem className="text-want" onClick={() => onDeleteLibrary(l)}>
                      <Trash2 className="mr-2 h-3.5 w-3.5" /> Delete library
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      ))}
      {libraries.length === 0 && !collapsed && (
        canManage ? (
          <button onClick={onAddLibrary} className="mx-3 rounded-[10px] border border-dashed px-3 py-2 text-left text-[12.5px] text-faint hover:border-brass/50 hover:text-brass">
            + Add your first library
          </button>
        ) : (
          <p className="mx-3 rounded-[10px] border border-dashed px-3 py-2 text-[12.5px] text-faint">
            No libraries assigned to you yet — ask an admin.
          </p>
        )
      )}

      <SectionHeader collapsed={collapsed} action={
        <button onClick={onAddScope} aria-label="Add scope"
          className="flex h-5 w-5 items-center justify-center rounded-full text-faint hover:bg-brass/15 hover:text-brass">
          <svg viewBox="0 0 24 24" width="13" height="13" style={{ stroke: "currentColor", fill: "none", strokeWidth: 2 }}><path d="M12 5v14M5 12h14" /></svg>
        </button>
      }>Scopes</SectionHeader>
      {SCOPES.map(s => (
        <button key={s.id} title={collapsed ? s.label : undefined}
          onClick={() => onSelectScope(s.id)}
          className={item(view === "library" && scope === s.id && !activeCustomScope)}>
          <s.icon className="h-4 w-4 shrink-0" />
          {!collapsed && s.label}
        </button>
      ))}
      {customScopes.map(cs => (
        <div key={cs.name} className="group relative flex items-center">
          <button title={collapsed ? cs.name : undefined}
            onClick={() => onSelectCustomScope(cs)}
            className={cn(item(view === "library" && activeCustomScope === cs.name), !collapsed && "pr-7")}>
            <Bookmark className="h-4 w-4 shrink-0" />
            {!collapsed && <span className="truncate">{cs.name}</span>}
          </button>
          {!collapsed && (
            <button aria-label={`Delete scope ${cs.name}`}
              onClick={() => onDeleteCustomScope(cs.name)}
              className="absolute right-1.5 flex h-5 w-5 items-center justify-center rounded-md text-faint opacity-0 hover:bg-surface3 hover:text-want focus-visible:opacity-100 group-hover:opacity-100">
              <X className="h-3 w-3" />
            </button>
          )}
        </div>
      ))}
    </aside>
  )
}
