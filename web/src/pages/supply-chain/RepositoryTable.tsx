import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { TableCell, TableRow } from '@/components/ui/table'
import { getSupplyChainRepository } from '@/api/supply-chain'
import type { RepositorySummary } from '@/api/types'
import { DASH, humanize, licenseSummaryEntries, licenseTone, messageOf } from './format'
import { Pill, StatusPill } from './pill'

/** One repository: its inventory state, read on its own so a failure only affects this row. */
export function RepositoryRow({
  repository,
  stream,
  selected,
  refreshing,
  onInspect,
  onRefresh,
}: {
  repository: RepositorySummary
  stream: string
  selected: boolean
  refreshing: boolean
  onInspect: () => void
  onRefresh: () => void
}) {
  const query = useQuery({
    queryKey: ['supply-chain', 'repository', repository.github_id, stream],
    queryFn: ({ signal }) => getSupplyChainRepository(repository.github_id, { stream }, signal),
    retry: false,
  })
  const status = query.data
  const snapshot = status?.latest_snapshot
  const chips = licenseSummaryEntries(status?.license_summary)
  const name = repository.name || String(repository.github_id)

  return (
    <TableRow data-state={selected ? 'selected' : undefined}>
      <TableCell className="font-mono break-all whitespace-normal">{name}</TableCell>
      <TableCell>
        {status ? <StatusPill value={status.collection} label="Collection state" /> : query.isError ? <span className="text-destructive">{messageOf(query.error)}</span> : <span className="text-muted-foreground">Loading…</span>}
      </TableCell>
      <TableCell>{status ? humanize(status.freshness_seconds) : DASH}</TableCell>
      <TableCell>{snapshot ? snapshot.component_count : DASH}</TableCell>
      <TableCell>{status ? (snapshot ? snapshot.warning_count : 0) : DASH}</TableCell>
      <TableCell className="whitespace-normal">
        {status ? (
          chips.length ? (
            <span className="flex flex-wrap gap-1">
              {chips.map(({ status: value, count }) => (
                <span key={value} className="inline-flex items-center gap-1">
                  <span className="font-mono text-xs">{count}</span>
                  <Pill value={value} tone={licenseTone(value)} label="Assessment" />
                </span>
              ))}
            </span>
          ) : (
            'no assessments'
          )
        ) : (
          DASH
        )}
      </TableCell>
      <TableCell className="whitespace-normal">
        <div className="flex flex-wrap gap-1">
          <Button type="button" size="sm" variant={selected ? 'secondary' : 'outline'} aria-pressed={selected} onClick={onInspect}>
            Inspect <span className="sr-only">{name}</span>
          </Button>
          <Button type="button" size="sm" variant="outline" disabled={refreshing} onClick={onRefresh}>
            Refresh now <span className="sr-only">{name}</span>
          </Button>
        </div>
      </TableCell>
    </TableRow>
  )
}
