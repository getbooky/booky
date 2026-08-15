import { useMemo, useState } from "react"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api } from "@/api"
import { useApi } from "@/hooks/use-api"
import { FilterRows } from "@/components/FilterRows"
import type { ScopeFilter } from "@/components/FilterRows"

export function AddScopeDialog({ open, onOpenChange, onSave }: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onSave: (name: string, filters: ScopeFilter[]) => void
}) {
  const [name, setName] = useState("")
  const [rows, setRows] = useState<ScopeFilter[]>([{ field: "Genre", op: "is", value: "" }])
  const { data } = useApi(() => api.books())
  const books = useMemo(() => data?.books ?? [], [data])

  const save = () => {
    const filters = rows.filter(r => r.value.trim() !== "")
    if (!name.trim() || filters.length === 0) return
    onSave(name.trim(), filters)
    setName("")
    setRows([{ field: "Genre", op: "is", value: "" }])
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md rounded-xl">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">New scope</DialogTitle>
          <p className="text-xs text-muted-foreground">A scope is a saved filter — it lives in the sidebar and always shows the books that match. "is" values in the same field match any of them, "is not" always excludes, and different fields must all match.</p>
        </DialogHeader>
        <div className="grid gap-4 py-1">
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">Name</div>
            <Input autoFocus value={name} onChange={e => setName(e.target.value)} placeholder="Sci-fi on shelf" />
          </div>
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">Filters</div>
            <FilterRows rows={rows} onChange={setRows} books={books} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" className="" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button className="" disabled={!name.trim() || !rows.some(r => r.value.trim() !== "")} onClick={save}>Save scope</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
