import { getAdminJobs, retryJob } from '@/api/admin'
import type { AdminJob, AdminJobList } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { DASH, short, when } from './format'
import { EmptyRow, ErrorNotice, StatCard, StatusPill, mono } from './shared'
import { useAdminActions } from './use-admin-actions'
import { useCursorPages } from './use-cursor-pages'

const fetchPage = (cursor: string | undefined, signal: AbortSignal) => getAdminJobs(cursor, signal)
const select = (response: AdminJobList) => response.jobs
const idOf = (job: AdminJob) => job.id

const states = ['queued', 'running', 'succeeded', 'failed', 'superseded'] as const

function cardTone(state: string, count: number) {
  if (count === 0 || state === 'succeeded') return undefined
  return state === 'failed' || state === 'superseded' ? 'err' : 'warn'
}

export function JobsSection() {
  const pages = useCursorPages({ queryKey: ['admin', 'jobs'], fetchPage, select, idOf, refetchInterval: 30_000 })
  const { confirmed, dialog } = useAdminActions()
  const jobs = pages.items

  if (pages.first.data === undefined) {
    return pages.first.isError ? (
      <ErrorNotice error={pages.first.error} onRetry={() => void pages.first.refetch()} />
    ) : (
      <Skeleton className="h-32 w-full" aria-label="Loading jobs" />
    )
  }

  return (
    <div className="grid gap-4">
      {pages.first.isError && <ErrorNotice error={pages.first.error} />}
      <div className="grid gap-3 sm:grid-cols-3 lg:grid-cols-5">
        {states.map((state) => {
          const count = jobs.filter((job) => job.state === state).length
          return <StatCard key={state} label={state} value={count} tone={cardTone(state, count)} />
        })}
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead scope="col">Job</TableHead>
            <TableHead scope="col">Repository</TableHead>
            <TableHead scope="col">Target ref</TableHead>
            <TableHead scope="col">Target SHA</TableHead>
            <TableHead scope="col">State</TableHead>
            <TableHead scope="col">Attempts</TableHead>
            <TableHead scope="col">Reason</TableHead>
            <TableHead scope="col">Error code</TableHead>
            <TableHead scope="col">Updated</TableHead>
            <TableHead scope="col">Action</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {jobs.length === 0 && <EmptyRow columns={10}>No index jobs are available.</EmptyRow>}
          {jobs.map((job) => (
            <TableRow key={job.id}>
              <TableCell>#{job.id}</TableCell>
              <TableCell className={mono}>{job.repository}</TableCell>
              <TableCell className="break-all whitespace-normal">{job.target_ref || DASH}</TableCell>
              <TableCell className="font-mono">{short(job.target_sha)}</TableCell>
              <TableCell>
                <StatusPill value={job.state} />
              </TableCell>
              <TableCell>
                {job.attempt} / {job.max_attempts}
              </TableCell>
              <TableCell>{job.reason || DASH}</TableCell>
              <TableCell>{job.error_code || DASH}</TableCell>
              <TableCell>{when(job.updated_at)}</TableCell>
              <TableCell>
                {job.state === 'failed' ? (
                  <Button type="button" size="sm" variant="outline" onClick={() => void confirmed(`Retry failed job #${job.id}?`, () => retryJob(job.id), 'Job retry queued.')}>
                    Retry
                  </Button>
                ) : (
                  DASH
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {pages.cursor && (
        <div>
          <Button type="button" variant="outline" disabled={pages.loadingMore} onClick={pages.loadMore}>
            Load older jobs
          </Button>
        </div>
      )}
      {dialog}
    </div>
  )
}
