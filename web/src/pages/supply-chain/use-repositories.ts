import { useQuery } from '@tanstack/react-query'
import { listRepositories } from '@/api/repositories'
import type { RepositorySummary } from '@/api/types'

/** The legacy page read up to this many authorized repositories. */
const REPOSITORY_LIMIT = 1000

/** Authorized repositories, read page by page up to the limit. The query signal cancels the chain. */
export function useSupplyChainRepositories() {
  return useQuery({
    queryKey: ['supply-chain', 'repositories'],
    queryFn: async ({ signal }) => {
      const repositories: RepositorySummary[] = []
      let cursor: string | undefined
      do {
        const page = await listRepositories(cursor, signal)
        repositories.push(...(Array.isArray(page.repositories) ? page.repositories : []))
        cursor = page.truncated && page.next_cursor && repositories.length < REPOSITORY_LIMIT ? page.next_cursor : undefined
      } while (cursor)
      return repositories.slice(0, REPOSITORY_LIMIT)
    },
    retry: false,
  })
}
