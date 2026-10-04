import { useQuery } from '@tanstack/react-query'
import { getAdminOverview } from '@/api/admin'

/**
 * GET /v1/admin/overview doubles as the administrator probe, the sidebar gate and the overview data.
 * Every admin mutation invalidates the "admin" scope, which re-probes it. Only the console polls.
 */
export function useAdminOverview(poll = false) {
  return useQuery({
    queryKey: ['admin', 'overview'],
    queryFn: ({ signal }) => getAdminOverview(signal),
    retry: false,
    staleTime: poll ? 0 : Infinity,
    refetchInterval: poll ? 30_000 : false,
  })
}
