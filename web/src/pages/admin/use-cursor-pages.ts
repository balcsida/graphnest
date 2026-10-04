import { useCallback, useEffect, useRef, useState } from 'react'
import { useQuery, type QueryKey, type UseQueryResult } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError } from '@/lib/api'
import { bounded, errorMessage, isAbort } from './format'

export interface Page<T> {
  items: T[]
  truncated: boolean
  next_cursor?: string
}

export interface CursorPages<T> {
  first: UseQueryResult<Page<T>>
  /** First page followed by every loaded page, de-duplicated by id, in server order. */
  items: T[]
  cursor: string | null
  loadingMore: boolean
  loadMore: () => void
}

/**
 * The first page is a polled query; "load more" follows the cursor of the last loaded page.
 * A refresh of the first page wins: pages loaded before it are dropped and an in-flight
 * "load more" is aborted, so a stale cursor can never replace fresh data.
 */
export function useCursorPages<T, R extends { truncated: boolean; next_cursor?: string }>(options: {
  queryKey: QueryKey
  fetchPage: (cursor: string | undefined, signal: AbortSignal) => Promise<R>
  select: (response: R) => T[]
  idOf: (item: T) => string | number
  refetchInterval?: number
}): CursorPages<T> {
  const { queryKey, fetchPage, select, idOf, refetchInterval } = options
  const first = useQuery({
    queryKey,
    queryFn: async ({ signal }): Promise<Page<T>> => {
      const response = await fetchPage(undefined, signal)
      return { items: bounded(select(response)), truncated: response.truncated, next_cursor: response.next_cursor }
    },
    refetchInterval,
    retry: false,
  })
  const [more, setMore] = useState<{ source: Page<T> | undefined; pages: Page<T>[] }>({ source: undefined, pages: [] })
  const [loadingSource, setLoadingSource] = useState<Page<T> | undefined>(undefined)
  const abort = useRef<AbortController | null>(null)
  const source = first.data

  // A different first page invalidates whatever was loaded after it.
  const pages = more.source === source ? more.pages : []
  const last = pages.at(-1) ?? source
  const cursor = last?.next_cursor ?? null

  // Aborting settles the pending request without touching state; the new source is no longer "loading".
  useEffect(() => {
    abort.current?.abort()
  }, [source])
  useEffect(() => () => abort.current?.abort(), [])

  const loadMore = useCallback(() => {
    if (!source || !cursor) return
    abort.current?.abort()
    const controller = (abort.current = new AbortController())
    setLoadingSource(source)
    fetchPage(cursor, controller.signal)
      .then((response) => {
        if (controller.signal.aborted) return
        const page = { items: bounded(select(response)), truncated: response.truncated, next_cursor: response.next_cursor }
        setMore((current) => ({ source, pages: [...(current.source === source ? current.pages : []), page] }))
      })
      .catch((failure: unknown) => {
        if (controller.signal.aborted || isAbort(failure)) return
        if (!(failure instanceof ApiError && failure.status === 401)) toast.error(errorMessage(failure))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoadingSource(undefined)
      })
  }, [source, cursor, fetchPage, select])

  const seen = new Set<string | number>()
  const items: T[] = []
  for (const item of [...(source?.items ?? []), ...pages.flatMap((page) => page.items)]) {
    const id = idOf(item)
    if (seen.has(id)) continue
    seen.add(id)
    items.push(item)
  }
  return { first, items, cursor, loadingMore: loadingSource === source && source !== undefined, loadMore }
}
