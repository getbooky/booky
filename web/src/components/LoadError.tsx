import { Button } from "@/components/ui/button"

// LoadError is what a view shows when its data didn't arrive. Without it a
// failed load looks exactly like an empty shelf ("Nothing yet"), which is a
// different thing to be told.
export function LoadError({ what, error, onRetry }: { what: string; error: string; onRetry: () => void }) {
  return (
    <div role="alert" className="mb-4 flex flex-wrap items-center gap-3 rounded-r-lg border-l-[3px] border-want bg-want/10 px-4 py-3 text-[13px]">
      <span className="min-w-0 flex-1">Couldn't load {what}: {error}</span>
      <Button variant="outline" className="h-8" onClick={onRetry}>Try again</Button>
    </div>
  )
}
