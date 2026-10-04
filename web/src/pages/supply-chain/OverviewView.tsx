import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Skeleton } from '@/components/ui/skeleton'
import { getSupplyChainOverview } from '@/api/supply-chain'
import { ErrorNotice, Panel } from '@/pages/admin/shared'
import { LICENSE_ORDER, denominatorText, isDisabled, licenseTone, text, when } from './format'
import { useViewParams } from './params'
import { Pill } from './pill'
import { DisabledModule, MetricCard, StateMessage, StreamSelect, ViewHeader } from './shared'
import type { SupplyChainOverview } from '@/api/types'

function Line({ head, count }: { head: ReactNode; count: number }) {
  return (
    <p className="flex items-center gap-2">
      {head}
      <span className="font-mono">{count}</span>
    </p>
  )
}

/** Statuses with a count, in the stable display order; "unassessed" comes from the component totals. */
const assessmentLines = (overview: SupplyChainOverview) =>
  LICENSE_ORDER.flatMap((status) => {
    const count = status === 'unassessed' ? Number(overview.components.unassessed) : Number(overview.components.assessments?.[status])
    return Number.isFinite(count) && count > 0 ? [{ status, count }] : []
  })

function Cards({ overview }: { overview: SupplyChainOverview }) {
  const { repositories, components } = overview
  const authorized = `authorized repositories`
  return (
    <>
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6" role="group" aria-label="Repositories">
        <MetricCard label="Authorized repositories" value={repositories.authorized} denominator="repositories this token can read" />
        <MetricCard label="With inventory" value={repositories.with_inventory} denominator={denominatorText(repositories.with_inventory, repositories.authorized, authorized)} />
        <MetricCard label="Never collected" value={repositories.never_collected} denominator={denominatorText(repositories.never_collected, repositories.authorized, authorized)} />
        <MetricCard label="Stale" value={repositories.stale} denominator={denominatorText(repositories.stale, repositories.authorized, authorized)} />
        <MetricCard label="Failed last attempt" value={repositories.failed_last_attempt} denominator={denominatorText(repositories.failed_last_attempt, repositories.authorized, authorized)} />
        <MetricCard label="Opted out" value={repositories.opted_out} denominator={denominatorText(repositories.opted_out, repositories.authorized, authorized)} />
      </div>
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4" role="group" aria-label="Components">
        <MetricCard label="Occurrences" value={components.occurrences} denominator="component rows in the latest snapshot of each repository" />
        <MetricCard label="Unique coordinates" value={components.unique_coordinates} denominator={denominatorText(components.unique_coordinates, components.occurrences, 'occurrences')} />
        <MetricCard label="Without purl" value={components.without_purl} denominator={denominatorText(components.without_purl, components.occurrences, 'occurrences')} />
        <MetricCard label="Without version" value={components.without_version} denominator={denominatorText(components.without_version, components.occurrences, 'occurrences')} />
      </div>
    </>
  )
}

export default function OverviewView() {
  const { stream, update } = useViewParams()
  const query = useQuery({
    queryKey: ['supply-chain', 'overview', stream],
    queryFn: ({ signal }) => getSupplyChainOverview({ stream }, signal),
    retry: false,
  })
  const overview = query.data

  return (
    <div className="grid gap-4">
      <ViewHeader title="Overview" />
      <StreamSelect stream={stream} onChange={(value) => update({ stream: value })} />
      {query.isError && isDisabled(query.error) ? (
        <DisabledModule error={query.error} />
      ) : (
        <>
          <div aria-live="polite">
            {query.isPending && <StateMessage>Loading overview…</StateMessage>}
            {overview && overview.repositories.authorized === 0 && <StateMessage>No authorized repositories are in scope</StateMessage>}
          </div>
          {query.isError && <ErrorNotice error={query.error} onRetry={() => void query.refetch()} />}
          {query.isPending && <Skeleton className="h-32 w-full" aria-label="Loading overview" />}
          {overview && (
            <>
              <Cards overview={overview} />
              <div className="grid gap-4 md:grid-cols-2">
                <Panel title="Assessments">
                  {assessmentLines(overview).length ? (
                    assessmentLines(overview).map(({ status, count }) => <Line key={status} head={<Pill value={status} tone={licenseTone(status)} label="Assessment" />} count={count} />)
                  ) : (
                    <p className="text-muted-foreground">No assessments have been recorded.</p>
                  )}
                </Panel>
                <Panel title="Ecosystems">
                  {overview.ecosystems.length ? (
                    overview.ecosystems.map((facet) => <Line key={facet.value} head={<span>{text(facet.value)}</span>} count={facet.count} />)
                  ) : (
                    <p className="text-muted-foreground">No ecosystems have been observed.</p>
                  )}
                </Panel>
                <Panel title="Observation window">
                  <p>Oldest: {when(overview.oldest_collected_at)}</p>
                  <p>Newest: {when(overview.newest_collected_at)}</p>
                  <p>Parser warnings: {overview.warning_total}</p>
                </Panel>
                <Panel title="How these numbers are counted">
                  {overview.denominators.length ? (
                    overview.denominators.map((line, index) => (
                      <p key={index} className="text-muted-foreground">
                        {line}
                      </p>
                    ))
                  ) : (
                    <p className="text-muted-foreground">No denominators were reported.</p>
                  )}
                </Panel>
              </div>
            </>
          )}
        </>
      )}
    </div>
  )
}
