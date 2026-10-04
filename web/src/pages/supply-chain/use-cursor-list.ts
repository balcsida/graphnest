import { useInfiniteQuery, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { ApiError } from '@/lib/api'

/**
 * A list read page by page with the server's cursor. The cursor belongs to the filter set that produced it:
 * every filter is part of `queryKey`, so any filter change starts a new query at the first page (the previous
 * pages are dropped, not cached). A cursor the server rejects (400) silently restarts from the first page.
 */
export function useCursorList<R extends { truncated: boolean; next_cursor?: string }>(options: {
  queryKey: QueryKey
  fetchPage: (cursor: string | undefined, signal: AbortSignal) => Promise<R>
  enabled?: boolean
}) {
  const { queryKey, fetchPage, enabled } = options
  const queryClient = useQueryClient()
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ pageParam, signal }) => fetchPage(pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.truncated && last.next_cursor ? last.next_cursor : undefined),
    enabled,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false,
  })
  async function loadMore() {
    const result = await query.fetchNextPage()
    if (result.isError && result.error instanceof ApiError && result.error.status === 400) await queryClient.resetQueries({ queryKey, exact: true })
  }
  return { query, pages: query.data?.pages ?? [], hasMore: query.hasNextPage, loadingMore: query.isFetchingNextPage, loadMore }
}
