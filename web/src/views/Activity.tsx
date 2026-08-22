import { useMemo, useState } from "react"
import { Tag, Folio } from "@/components/bits"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { acquisition } from "@/api"
import type { ApiQueueItem } from "@/api"
import { useApi } from "@/hooks/use-api"
import { ManualImportDialog } from "@/components/BookDetail"
import { FolderInput, RefreshCw, X } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { formatAge, formatFull, formatWhen } from "@/lib/time"

const tabCls = "rounded-none border-b-2 border-transparent px-0.5 pb-2.5 pt-0 text-sm font-semibold data-[state=active]:border-brass data-[state=active]:bg-transparent data-[state=active]:shadow-none"

// An imported row leaves the queue entirely (the server deletes it), so there
// is no "done" state to render here — Imported lives in History.
const statusTag: Record<string, { kind: "good" | "info" | "want" | "dim"; text: string }> = {
  queued:          { kind: "dim", text: "Queued" },
  downloading:     { kind: "info", text: "Downloading" },
  importing:       { kind: "info", text: "Importing" },
  failed:          { kind: "want", text: "Failed" },
  import_failed:   { kind: "want", text: "Import failed" },
}

const kindDot: Record<string, string> = {
  imported: "bg-good", grabbed: "bg-info", added: "bg-brass",
  warning: "bg-want", removed: "bg-want", blocked: "bg-want",
  failed: "bg-want", "import failed": "bg-want",
}

export function ActivityView() {
  const queueQuery = useApi(() => acquisition.queue(), 5_000)
  const historyQuery = useApi(() => acquisition.history(), 10_000)
  const queue = useMemo(() => queueQuery.data?.queue ?? [], [queueQuery.data])
  const history = useMemo(() => historyQuery.data?.history ?? [], [historyQuery.data])
  const active = queue.filter(q => q.status !== "failed" && q.status !== "import_failed").length
  // failed row being hand-resolved via manual import
  const [importItem, setImportItem] = useState<ApiQueueItem | null>(null)

  return (
    <section>
      <Folio title="Activity" end={
        <Button variant="outline" className="h-8" onClick={() => { queueQuery.reload(); historyQuery.reload() }}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" /> Refresh
        </Button>
      } />
      <Tabs defaultValue="queue">
        <TabsList className="h-auto w-full justify-start gap-6 rounded-none border-b bg-transparent p-0">
          <TabsTrigger value="queue" className={tabCls}>
            Queue {active > 0 && <span className="ml-1.5 text-faint">{active}</span>}
          </TabsTrigger>
          <TabsTrigger value="history" className={tabCls}>History</TabsTrigger>
        </TabsList>

        <TabsContent value="queue" className="mt-4">
          {queue.length === 0 && !queueQuery.loading && (
            <p className="text-[13.5px] text-muted-foreground">
              The queue is empty — grabs show up here the moment they start, and drop off once they're imported. History keeps the record.
            </p>
          )}
          <div className="border-t">
            {queue.map(q => {
              const tag = statusTag[q.status] ?? { kind: "dim" as const, text: q.status }
              return (
                <div key={q.id} className="grid grid-cols-[1fr_auto] items-center gap-4 border-b border-linesoft px-1 py-3">
                  <div className="min-w-0">
                    <div className="text-[13.5px] font-semibold">{q.bookTitle}</div>
                    <div className="mt-0.5 truncate text-xs text-muted-foreground">{q.releaseTitle}</div>
                    <div className="mt-0.5 text-xs text-faint">
                      {q.source} · {q.protocol}{q.detail ? ` · ${q.detail}` : ""}
                    </div>
                  </div>
                  <div className="flex flex-col items-end gap-1.5">
                    <span className="flex flex-wrap items-center justify-end gap-2">
                      {/* only a failed IMPORT has a file on disk to act on —
                          a failed download has nothing to retry or hand-pick */}
                      {q.status === "import_failed" && (
                        <>
                          <Button variant="outline" className="h-7 px-2 text-[12px]"
                            title="Deliver the already-downloaded file again — fix the cause first (permissions, disk space)"
                            onClick={async () => {
                              try {
                                await acquisition.retryImport(q.id)
                                toast.success("Retrying the import — watch this row")
                                queueQuery.reload()
                              } catch (e) { toast.error(`${e instanceof Error ? e.message : e}`) }
                            }}>
                            <RefreshCw className="mr-1 h-3 w-3" /> Retry import
                          </Button>
                          <Button variant="outline" className="h-7 px-2 text-[12px]"
                            title="Pick the file yourself and deliver it — resolves this row"
                            onClick={() => setImportItem(q)}>
                            <FolderInput className="mr-1 h-3 w-3" /> Manual import
                          </Button>
                        </>
                      )}
                      <Tag kind={tag.kind}>{tag.text}</Tag>
                      {/* cancel stops the download where it lives (SAB job
                          with its files, in-flight direct fetch, or a file
                          waiting for import) — no blocklist, no cascade, so
                          the release can still be grabbed again by hand */}
                      <Button variant="ghost" className="h-7 w-7 p-0 text-faint hover:text-want"
                        title={q.status === "failed" ? "Dismiss this row"
                          : q.status === "import_failed" ? "Give up on this download — deletes the downloaded file"
                          : "Cancel this download — removes its files"}
                        onClick={async () => {
                          try {
                            await acquisition.cancelQueue(q.id)
                            toast.success(q.status === "failed" ? "Dismissed" : "Cancelled")
                            queueQuery.reload(); historyQuery.reload()
                          } catch (e) { toast.error(`${e instanceof Error ? e.message : e}`) }
                        }}>
                        <X className="h-3.5 w-3.5" />
                      </Button>
                    </span>
                    {/* when this row last moved, and how long it's been sitting
                        there — the two things a stuck queue gets asked */}
                    <span className="font-label whitespace-nowrap text-[10.5px] tracking-[0.04em] text-faint"
                      title={`Grabbed ${formatFull(q.createdAt)}\nLast update ${formatFull(q.updatedAt)}`}>
                      {formatWhen(q.updatedAt)} · {formatAge(q.updatedAt)}
                    </span>
                  </div>
                </div>
              )
            })}
          </div>
        </TabsContent>

        <TabsContent value="history" className="mt-4">
          {history.length === 0 && !historyQuery.loading && (
            <p className="text-[13.5px] text-muted-foreground">Nothing yet — adds, grabs, and imports all leave a trail here.</p>
          )}
          <div className="border-t">
            {history.map(h => (
              <div key={h.id} className="grid grid-cols-[14px_1fr_auto] items-baseline gap-3 border-b border-linesoft px-1 py-2.5 text-[13px]">
                <span className={cn("relative -top-px h-[7px] w-[7px] justify-self-center rounded-full", kindDot[h.kind] ?? "bg-faint")} />
                <div className="min-w-0 truncate">
                  {h.bookTitle && <b>{h.bookTitle}</b>} <span className="text-muted-foreground">{h.kind}</span>
                  {h.detail && <span className="text-muted-foreground"> — {h.detail}</span>}
                </div>
                <span className="font-label whitespace-nowrap text-[10.5px] tracking-[0.04em] text-faint"
                  title={formatFull(h.createdAt)}>{formatWhen(h.createdAt)}</span>
              </div>
            ))}
          </div>
        </TabsContent>
      </Tabs>

      {importItem && (
        <ManualImportDialog open onOpenChange={v => { if (!v) setImportItem(null) }}
          bookId={importItem.bookId} bookTitle={importItem.bookTitle}
          defaultLibraryId={importItem.libraryId} queueId={importItem.id}
          initialPath={importItem.protocol === "direct" ? importItem.externalId : ""}
          onImported={() => { setImportItem(null); queueQuery.reload(); historyQuery.reload() }} />
      )}
    </section>
  )
}
