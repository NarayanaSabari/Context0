/**
 * @file use-poll.ts
 * Minimal polling hook used in place of a full query-caching library.
 * The app only needs two reads (health, memories) with no mutations or
 * cache invalidation, so a manual interval + refetch-on-key-change is
 * sufficient.
 */

import { useEffect, useRef, useState } from 'react'

/** How often a poll re-runs, in milliseconds. */
const INTERVAL_MS = 30_000

/**
 * Runs `fetchFn` immediately, then again every 30s, and again whenever
 * `refreshKey` changes. A failed fetch is swallowed (the fetcher is expected to
 * handle/log its own errors) and simply leaves `data` at its previous value.
 *
 * Effect cleanup clears the interval and ignores in-flight results, so this is
 * safe under `StrictMode`'s double-invoke in development.
 */
export function usePoll<T>(
  fetchFn: () => Promise<T>,
  refreshKey: number,
): { data: T | undefined; isFetching: boolean } {
  const [data, setData] = useState<T | undefined>(undefined)
  const [isFetching, setIsFetching] = useState(false)
  const fetchFnRef = useRef(fetchFn)
  fetchFnRef.current = fetchFn

  useEffect(() => {
    let cancelled = false

    const run = async () => {
      setIsFetching(true)
      try {
        const result = await fetchFnRef.current()
        if (!cancelled) setData(result)
      } catch {
        // Fetcher is responsible for surfacing its own errors; keep polling.
      } finally {
        if (!cancelled) setIsFetching(false)
      }
    }

    run()
    const id = setInterval(run, INTERVAL_MS)

    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [refreshKey])

  return { data, isFetching }
}
