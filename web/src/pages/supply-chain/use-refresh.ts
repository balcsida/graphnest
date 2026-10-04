import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getSupplyChainJob, refreshSupplyChainRepository } from '@/api/supply-chain'
import { ApiError } from '@/lib/api'
import { messageOf } from './format'

export type Say = (message: string, bad?: boolean) => void

const POLL_MS = 3000
/** About five minutes of polling; after that the job state is whatever was last shown. */
const POLL_LIMIT = 100
const finished = (state: string | undefined) => state === 'succeeded' || state === 'failed' || state === 'cancelled' || state === 'superseded'

/**
 * Queues a refresh (administrator only) and follows the job until it finishes, announcing
 * "Refresh job #N is {state}." through `say`. A finished job reloads the inventory.
 */
export function useRefresh(stream: string, say: Say) {
  const queryClient = useQueryClient()
  const [jobId, setJobId] = useState<number | null>(null)
  const mutation = useMutation({
    mutationFn: (repositoryId: number) => refreshSupplyChainRepository(repositoryId, { stream }),
    onSuccess: ({ job }) => {
      setJobId(job.id)
      say(`Refresh job #${job.id} is ${job.state}.`)
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 401) return
      say(error instanceof ApiError && error.status === 403 ? 'administrator access required' : messageOf(error), true)
    },
  })
  const job = useQuery({
    queryKey: ['supply-chain', 'job', jobId],
    queryFn: ({ signal }) => getSupplyChainJob(jobId as number, signal),
    enabled: jobId !== null,
    refetchInterval: (query) => (finished(query.state.data?.state) || query.state.dataUpdateCount >= POLL_LIMIT ? false : POLL_MS),
    retry: false,
  })
  const polled = job.data
  useEffect(() => {
    if (!polled) return
    say(`Refresh job #${polled.id} is ${polled.state}.`)
    if (finished(polled.state)) void queryClient.invalidateQueries({ queryKey: ['supply-chain'], predicate: (query) => query.queryKey[1] !== 'job' })
  }, [polled, say, queryClient])
  return { refresh: (repositoryId: number) => mutation.mutate(repositoryId), pendingRepositoryId: mutation.isPending ? mutation.variables : undefined }
}
