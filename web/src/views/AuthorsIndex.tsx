import { useState } from "react"
import { api } from "@/api"
import type { ApiAuthor } from "@/api"
import { useApi } from "@/hooks/use-api"
import { AuthorAvatar } from "@/components/AuthorAvatar"
import { Folio, Chips } from "@/components/bits"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Input } from "@/components/ui/input"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator,
  DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { MoreVertical, RefreshCw, Search, Trash2 } from "lucide-react"
import { useIsAdmin } from "@/lib/access"
import { toast } from "sonner"

export function AuthorsIndexView({ onOpenAuthor }: { onOpenAuthor: (a: ApiAuthor) => void }) {
  const isAdmin = useIsAdmin()
  const [filter, setFilter] = useState<"all" | "monitored">("all")
  const [sort, setSort] = useState("name")
  const [query, setQuery] = useState("")
  const { data, loading, error, reload } = useApi(() => api.authors(), 10_000)
  const authors = data?.authors ?? []
  const monitored = authors.filter(a => a.monitored).length
  const q = query.trim().toLowerCase()
  const rows = authors
    .filter(a => (!q || a.name.toLowerCase().includes(q)) && (filter === "all" ? true : a.monitored))
    .sort((a, b) => {
      if (sort === "books") return b.bookCount - a.bookCount
      if (sort === "shelf") return b.onShelf - a.onShelf
      return a.sortName.localeCompare(b.sortName)
    })

  const searchMissing = async (a: ApiAuthor) => {
    try {
      const r = await api.searchAuthor(a.id)
      toast.success(r.queued > 0
        ? `Searching ${r.queued} book${r.queued === 1 ? "" : "s"} by ${a.name} — watch Activity`
        : `Nothing to search — every monitored ${a.name} book is on the shelf`)
    } catch (e) {
      toast.error(`Search failed: ${e instanceof Error ? e.message : e}`)
    }
  }

  const refreshBibliography = async (a: ApiAuthor) => {
    toast(`Refreshing ${a.name}…`)
    try {
      const r = await api.expandAuthor(a.id)
      toast.success(r.added > 0 ? `${r.added} new book${r.added === 1 ? "" : "s"} for ${a.name}` : "Up to date")
      reload()
    } catch (e) {
      toast.error(`Refresh failed: ${e instanceof Error ? e.message : e}`)
    }
  }

  const remove = async (a: ApiAuthor, mode: "catalog" | "files") => {
    const what = mode === "files"
      ? `Remove ${a.name}, all their books, AND DELETE their files on disk (author folder included)?`
      : `Remove ${a.name} and all their books from Booky?\n\nFiles on disk are kept.`
    if (!window.confirm(what)) return
    try {
      const r = await api.deleteAuthor(a.id, mode)
      toast(mode === "files"
        ? `${a.name} removed — ${r.deletedFiles} file${r.deletedFiles === 1 ? "" : "s"} deleted`
        : `${a.name} removed — files on disk untouched`)
      reload()
    } catch (e) {
      toast.error(`Couldn't remove: ${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <section>
      <Folio
        title="Authors"
        meta={loading ? "loading…" : `${authors.length} authors · ${monitored} monitored`}
        end={
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative w-full max-w-[220px]">
              <Search className="absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
              <Input value={query} onChange={e => setQuery(e.target.value)}
                placeholder="Search authors…" className="h-8 pl-9 text-[13px]" />
            </div>
            <Select value={sort} onValueChange={setSort}>
              <SelectTrigger className="h-8 w-[110px] rounded-[10px] text-[12.5px]"><SelectValue /></SelectTrigger>
              <SelectContent className="rounded-xl">
                <SelectItem value="name">Name</SelectItem>
                <SelectItem value="books">Most books</SelectItem>
                <SelectItem value="shelf">Most on shelf</SelectItem>
              </SelectContent>
            </Select>
            <Chips options={[{ v: "all", label: "All" }, { v: "monitored", label: "Monitored" }]} value={filter} onChange={setFilter} />
          </div>
        }
      />
      {error && <div className="border-l-[3px] border-want bg-want/10 px-4 py-3 text-[13px]">Couldn't load authors: {error}</div>}
      {!loading && !error && authors.length === 0 && (
        <p className="max-w-[52ch] text-[13.5px] text-muted-foreground">
          No authors yet — they appear automatically when books are added.
        </p>
      )}
      <div className="grid grid-cols-[repeat(auto-fill,minmax(158px,1fr))] gap-3 sm:grid-cols-[repeat(auto-fill,minmax(230px,1fr))] sm:gap-4">
        {rows.map(a => (
          <div
            key={a.id}
            role="button" tabIndex={0}
            onClick={() => onOpenAuthor(a)}
            onKeyDown={e => { if (e.key === "Enter") onOpenAuthor(a) }}
            className="group/author relative flex cursor-pointer flex-col items-center rounded-2xl border border-linesoft bg-surface p-3.5 pt-5 transition-colors hover:border-brass/40 sm:p-5 sm:pt-6"
          >
            <AuthorAvatar author={a} className="h-16 w-16 sm:h-20 sm:w-20" textClass="text-[26px]" />
            <div className="font-book mt-3 w-full truncate text-center text-[16.5px] font-bold group-hover/author:text-brass">{a.name}</div>
            <div className="font-label mt-0.5 text-[10px] tracking-[0.08em] text-faint">
              {a.bookCount} BOOKS · {a.onShelf} ON SHELF
            </div>
            <div className="mt-3 h-1.5 w-full overflow-hidden rounded-[2px] bg-surface3">
              <div className="h-full rounded-[2px] bg-good"
                style={{ width: `${a.bookCount ? (a.onShelf / a.bookCount) * 100 : 0}%` }} />
            </div>
            <span onClick={e => e.stopPropagation()}
              className="mt-3 flex w-full items-center justify-between gap-1">
              <span className="mono-label text-muted-foreground">{a.onShelf} of {a.bookCount} on shelf</span>
              <span className="flex items-center gap-1">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button aria-label={`${a.name} options`}
                    className="flex h-7 w-7 items-center justify-center rounded-lg text-muted-foreground opacity-0 transition-opacity hover:bg-black/10 focus-visible:opacity-100 group-hover/author:opacity-100 data-[state=open]:opacity-100">
                    <MoreVertical className="h-4 w-4" />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="min-w-[210px] rounded-xl">
                  <DropdownMenuItem onClick={() => searchMissing(a)}>
                    <Search className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Search missing books
                  </DropdownMenuItem>
                  <DropdownMenuItem onClick={() => refreshBibliography(a)}>
                    <RefreshCw className="mr-2 h-3.5 w-3.5 text-muted-foreground" /> Refresh bibliography
                  </DropdownMenuItem>
                  {isAdmin && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuSub>
                        <DropdownMenuSubTrigger className="text-want data-[state=open]:text-want">
                          <Trash2 className="mr-2 h-3.5 w-3.5" /> Remove author
                        </DropdownMenuSubTrigger>
                        <DropdownMenuSubContent className="rounded-xl">
                          <DropdownMenuItem onClick={() => remove(a, "catalog")}>Remove — keep files on disk</DropdownMenuItem>
                          <DropdownMenuItem onClick={() => remove(a, "files")} className="text-want">Remove &amp; delete files (author folder too)</DropdownMenuItem>
                        </DropdownMenuSubContent>
                      </DropdownMenuSub>
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
              </span>
            </span>
          </div>
        ))}
      </div>
    </section>
  )
}
