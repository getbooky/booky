import { useState } from "react"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { PathInput } from "@/components/PathInput"
import { api } from "@/api"
import { toast } from "sonner"

export function AddLibraryDialog({ open, onOpenChange, onCreated }: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: () => void
}) {
  const [name, setName] = useState("")
  const [rootPath, setRootPath] = useState("/data/books/")

  const create = async () => {
    try {
      await api.createLibrary(name.trim(), rootPath.trim())
      toast.success(`Library "${name.trim()}" created`)
      setName("")
      onCreated()
      onOpenChange(false)
    } catch (e) {
      toast.error(`Couldn't create: ${e instanceof Error ? e.message : e}`)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md rounded-xl">
        <DialogHeader>
          <DialogTitle className="font-book text-lg font-bold">Add library</DialogTitle>
          <p className="text-xs text-muted-foreground">A library is a root folder — keep it on the same filesystem as your downloads for instant imports.</p>
        </DialogHeader>
        <div className="grid gap-4 py-1">
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">Name</div>
            <Input autoFocus value={name} onChange={e => setName(e.target.value)} placeholder="Library name" />
          </div>
          <div>
            <div className="mono-label mb-1.5 text-muted-foreground">Root path</div>
            <PathInput value={rootPath} onChange={setRootPath} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" className="" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button className="" disabled={!name.trim() || !rootPath.trim()} onClick={create}>Create</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
