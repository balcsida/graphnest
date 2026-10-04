import { useMemo } from 'react'
import { useInfiniteQuery } from '@tanstack/react-query'
import { listRepositories } from '@/api/repositories'
import type { RepositorySummary } from '@/api/types'

export const repositoriesKey = ['repositories'] as const

/** Authorized repositories, one cursor page at a time and de-duplicated by name. Shared by the picker and the table. */
export function useRepositories() {
  const query = useInfiniteQuery({
    queryKey: repositoriesKey,
    queryFn: ({ pageParam, signal }) => listRepositories(pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.truncated && last.next_cursor ? last.next_cursor : undefined),
    staleTime: 30_000,
    retry: false,
  })

  const repositories = useMemo(() => {
    const seen = new Set<string>()
    const unique: RepositorySummary[] = []
    for (const page of query.data?.pages ?? []) {
      for (const repository of page.repositories) {
        if (typeof repository.name !== 'string' || seen.has(repository.name)) continue
        seen.add(repository.name)
        unique.push(repository)
      }
    }
    return unique
  }, [query.data])

  const last = query.data?.pages.at(-1)
  const hasMore = query.hasNextPage
  let status = ''
  if (query.isPending) status = 'Loading authorized repositories…'
  else if (query.isFetchingNextPage) status = 'Loading authorized repositories…'
  else if (query.isError) status = 'Repository list is unavailable. Search still covers every authorized repository.'
  else if (hasMore) status = `Showing the first ${repositories.length} authorized repositories.`
  else if (last?.truncated) status = 'Only the first authorized repositories are shown.'
  else if (repositories.length === 0) status = 'No authorized repositories are available for this token.'

  return {
    repositories,
    hasMore,
    status,
    isError: query.isError,
    isLoading: query.isPending || query.isFetchingNextPage,
    loadMore: () => {
      if (hasMore && !query.isFetchingNextPage) void query.fetchNextPage()
    },
  }
}
