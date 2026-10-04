import { useState } from 'react'
import { toast } from 'sonner'
import { getAdminRepositories, reconcileGitHub, reindexRepository } from '@/api/admin'
import type { AdminRepository, AdminRepositoryList } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ApiError } from '@/lib/api'
import { DASH, errorMessage, short, when } from './format'
import { EmptyRow, ErrorNotice, PartialNotice, StatusPill, mono } from './shared'
import { useAdminActions } from './use-admin-actions'
import { useAdminOverview } from './use-admin-overview'
import { useCursorPages } from './use-cursor-pages'

const fetchPage = (cursor: string | undefined, signal: AbortSignal) => getAdminRepositories(cursor, signal)
const select = (response: AdminRepositoryList) => response.repositories
const idOf = (repository: AdminRepository) => repository.github_id

/** Status totals come from the server overview so chips never reflect only the loaded page. */
function statusCounts(totals: Record<string, number> | undefined, loaded: AdminRepository[]): Record<string, number> {
  if (totals && typeof totals === 'object') {
    const counts: Record<string, number> = {}
    let all = 0
    for (const [status, count] of Object.entries(totals)) {
      if (typeof count !== 'number') continue
      counts[status] = count
      all += count
    }
    counts.all = all
    return counts
  }
  const counts: Record<string, number> = { all: loaded.length }
  for (const repository of loaded) counts[repository.status] = (counts[repository.status] ?? 0) + 1
  return counts
}

export function RepositoriesSection() {
  const pages = useCursorPages({ queryKey: ['admin', 'repositories'], fetchPage, select, idOf, refetchInterval: 30_000 })
  const overview = useAdminOverview().data
  const { confirmed, confirm, refresh, dialog } = useAdminActions()
  const [filter, setFilter] = useState('')
  const [status, setStatus] = useState('all')
  const [selected, setSelected] = useState<ReadonlySet<number>>(new Set())

  const loaded = pages.items
  const counts = statusCounts(overview?.repositories, loaded)
  const statuses = ['all', ...new Set([...Object.keys(counts).filter((key) => key !== 'all'), ...loaded.map((repository) => repository.status)])]
  const inStatus = loaded.filter((repository) => status === 'all' || repository.status === status)
  const query = filter.toLowerCase()
  const visible = inStatus.filter((repository) => repository.name.toLowerCase().includes(query))
  const selectedVisible = visible.filter((repository) => selected.has(repository.github_id))
  const total = counts[status] ?? 0

  if (pages.first.data === undefined) {
    return pages.first.isError ? (
      <ErrorNotice error={pages.first.error} onRetry={() => void pages.first.refetch()} />
    ) : (
      <Skeleton className="h-32 w-full" aria-label="Loading repositories" />
    )
  }

  const first = pages.first.data
  const toggle = (id: number, checked: boolean) =>
    setSelected((current) => {
      const next = new Set(current)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })

  async function reindexSelected() {
    if (selectedVisible.length === 0) {
      toast.error('Select at least one repository.')
      return
    }
    if (!(await confirm(`Queue ${selectedVisible.length} selected repositories for reindexing?`))) return
    try {
      for (const repository of selectedVisible) await reindexRepository(repository.github_id)
    } catch (failure) {
      if (!(failure instanceof ApiError && failure.status === 401)) toast.error(errorMessage(failure))
      return
    }
    toast.success('Selected repositories queued.')
    setSelected(new Set())
    await refresh()
  }

  return (
    <div className="grid gap-4">
      {pages.first.isError && <ErrorNotice error={pages.first.error} />}
      {first.truncated && !first.next_cursor && <PartialNotice names={['repositories']} />}
      <div className="flex flex-wrap items-center gap-2">
        <label className="sr-only" htmlFor="repo-filter">
          Filter repositories
        </label>
        <Input id="repo-filter" className="w-64" placeholder="Filter repositories" value={filter} onChange={(event) => setFilter(event.target.value)} />
        <div className="flex flex-wrap gap-1">
          {statuses.map((value) => (
            <Button key={value} type="button" size="sm" variant={status === value ? 'secondary' : 'outline'} aria-pressed={status === value} onClick={() => setStatus(value)}>
              {value === 'all' ? 'All' : value} · {counts[value] ?? 0}
            </Button>
          ))}
        </div>
        <Button type="button" variant="outline" className="ml-auto" onClick={() => void confirmed('Reconcile repositories from GitHub now?', reconcileGitHub, 'Reconciliation completed.')}>
          Reconcile GitHub
        </Button>
        <Button type="button" onClick={() => void reindexSelected()}>
          Reindex selected
        </Button>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead scope="col">
              <Checkbox
                aria-label="Select all visible repositories"
                checked={visible.length > 0 && selectedVisible.length === visible.length}
                onCheckedChange={(checked) => setSelected(checked === true ? new Set(visible.map((repository) => repository.github_id)) : new Set())}
              />
            </TableHead>
            <TableHead scope="col" className="text-right">
              GitHub ID
            </TableHead>
            <TableHead scope="col">Repository</TableHead>
            <TableHead scope="col">Branch</TableHead>
            <TableHead scope="col">Status</TableHead>
            <TableHead scope="col">Error code</TableHead>
            <TableHead scope="col">Desired SHA</TableHead>
            <TableHead scope="col">Indexed SHA</TableHead>
            <TableHead scope="col">Last indexed</TableHead>
            <TableHead scope="col">Action</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {visible.length === 0 && (
            <EmptyRow columns={10}>No repositories match this view.</EmptyRow>
          )}
          {visible.map((repository) => (
            <TableRow key={repository.github_id}>
              <TableCell>
                <Checkbox aria-label={`Select ${repository.name}`} checked={selected.has(repository.github_id)} onCheckedChange={(checked) => toggle(repository.github_id, checked === true)} />
              </TableCell>
              <TableCell className="text-right">{String(repository.github_id || DASH)}</TableCell>
              <TableCell className={mono}>{repository.name}</TableCell>
              <TableCell>{repository.default_branch || DASH}</TableCell>
              <TableCell>
                <StatusPill value={repository.status} />
              </TableCell>
              <TableCell>{repository.error_code || DASH}</TableCell>
              <TableCell className="font-mono">{short(repository.desired_sha)}</TableCell>
              <TableCell className="font-mono">{short(repository.indexed_sha)}</TableCell>
              <TableCell>{when(repository.last_indexed_at)}</TableCell>
              <TableCell>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => void confirmed(`Queue a fresh index for ${repository.name}?`, () => reindexRepository(repository.github_id), 'Reindex queued.')}
                >
                  {repository.status === 'error' || repository.status === 'failed' ? 'Retry' : 'Reindex'}
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-muted-foreground" role="status">
          {pages.cursor && total > inStatus.length ? `Showing ${inStatus.length} of ${total} repositories.` : ''}
        </span>
        {pages.cursor && (
          <Button type="button" variant="outline" disabled={pages.loadingMore} onClick={pages.loadMore}>
            Load more repositories
          </Button>
        )}
      </div>
      {dialog}
    </div>
  )
}
