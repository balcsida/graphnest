import { useCallback, useState } from 'react'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ErrorNotice } from '@/pages/admin/shared'
import { RepositoryDetail } from './RepositoryDetail'
import { RepositoryRow } from './RepositoryTable'
import { isDisabled } from './format'
import { idParam, useViewParams } from './params'
import { DisabledModule, StateMessage, StreamSelect, ViewHeader } from './shared'
import { useRefresh } from './use-refresh'
import { useSupplyChainRepositories } from './use-repositories'

/** Rows shown before "Show more repositories"; each row reads its own inventory state. */
const ROW_WINDOW = 25

export default function RepositoriesView() {
  const { params, stream, update } = useViewParams()
  const selectedId = idParam(params.get('repo'))
  const repositories = useSupplyChainRepositories()
  const [shown, setShown] = useState(ROW_WINDOW)
  const [status, setStatus] = useState<{ message: string; bad: boolean } | null>(null)
  const say = useCallback((message: string, bad = false) => setStatus({ message, bad }), [])
  const refresh = useRefresh(stream, say)

  if (repositories.isError && isDisabled(repositories.error)) {
    return (
      <div className="grid gap-4">
        <ViewHeader title="Repository inventory" />
        <DisabledModule error={repositories.error} />
      </div>
    )
  }
  const rows = repositories.data ?? []
  const selected = rows.find((repository) => repository.github_id === selectedId)

  return (
    <div className="grid gap-4">
      <ViewHeader title="Repository inventory" />
      <StreamSelect stream={stream} onChange={(value) => update({ stream: value })} />
      <div role="status" aria-live="polite">
        {status && !status.bad && <p className="text-sm">{status.message}</p>}
      </div>
      {status?.bad && (
        <Alert variant="destructive" aria-live="assertive">
          <AlertDescription>{status.message}</AlertDescription>
        </Alert>
      )}
      {repositories.isPending && (
        <>
          <StateMessage>Loading repositories…</StateMessage>
          <Skeleton className="h-32 w-full" aria-label="Loading repositories" />
        </>
      )}
      {repositories.isError && <ErrorNotice error={repositories.error} onRetry={() => void repositories.refetch()} />}
      {repositories.isSuccess && rows.length === 0 && <StateMessage>No repositories are available.</StateMessage>}
      {rows.length > 0 && (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                {['Repository', 'Collection state', 'Freshness', 'Components', 'Warnings', 'Assessments', 'Actions'].map((heading) => (
                  <TableHead key={heading} scope="col">
                    {heading}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.slice(0, shown).map((repository) => (
                <RepositoryRow
                  key={repository.github_id}
                  repository={repository}
                  stream={stream}
                  selected={repository.github_id === selectedId}
                  refreshing={refresh.pendingRepositoryId === repository.github_id}
                  onInspect={() => update({ repo: String(repository.github_id), q: undefined, element: undefined, snapshot: undefined })}
                  onRefresh={() => refresh.refresh(repository.github_id)}
                />
              ))}
            </TableBody>
          </Table>
          {shown < rows.length && (
            <Button type="button" variant="secondary" className="justify-self-start" onClick={() => setShown((count) => count + ROW_WINDOW)}>
              Show more repositories
            </Button>
          )}
        </>
      )}
      {selectedId !== undefined && (
        <RepositoryDetail key={`${selectedId}:${stream}`} repositoryId={selectedId} name={selected?.name ?? String(selectedId)} stream={stream} say={say} />
      )}
      {selectedId === undefined && rows.length > 0 && <StateMessage>Inspect a repository to see its components, warnings and collection history.</StateMessage>}
    </div>
  )
}
