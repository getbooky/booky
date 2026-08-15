import { useCallback, useEffect, useRef, useState } from "react"

// Minimal fetch-state hook: load on mount, expose reload() for after
// mutations. Pass refreshMs to also re-fetch quietly on an interval — the
// watcher adds books in the background, and views should reflect that
// without a manual page refresh (quiet = no loading flicker).
export function useApi<T>(fetcher: () => Promise<T>, refreshMs?: number) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const fetcherRef = useRef(fetcher)
  fetcherRef.current = fetcher

  const reload = useCallback((opts?: { quiet?: boolean }) => {
    const quiet = opts?.quiet === true
    if (!quiet) {
      setLoading(true)
      setError(null)
    }
    fetcherRef.current()
      .then(setData)
      .catch(e => { if (!quiet) setError(e instanceof Error ? e.message : String(e)) })
      .finally(() => { if (!quiet) setLoading(false) })
  }, [])

  useEffect(() => { reload() }, [reload])

  useEffect(() => {
    if (!refreshMs) return
    const id = setInterval(() => {
      if (!document.hidden) reload({ quiet: true })
    }, refreshMs)
    return () => clearInterval(id)
  }, [refreshMs, reload])

  // Coming back to the app is the moment stale data is most obvious, and the
  // one people reach for pull-to-refresh to fix. The interval above skips
  // hidden tabs — and a backgrounded PWA has its timers suspended outright —
  // so without this the first refresh after resuming is a whole interval
  // away. An installed iOS PWA has no reload gesture at all, which makes this
  // the only way its data ever catches up short of force-quitting.
  //
  // Only views that opted into polling get it: they're the ones showing live
  // data, and a view that didn't ask to re-fetch shouldn't start doing it
  // under the user mid-interaction.
  useEffect(() => {
    if (!refreshMs) return
    const onVisible = () => {
      if (!document.hidden) reload({ quiet: true })
    }
    document.addEventListener("visibilitychange", onVisible)
    return () => document.removeEventListener("visibilitychange", onVisible)
  }, [refreshMs, reload])

  return { data, error, loading, reload }
}
