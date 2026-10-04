import { useQuery } from '@tanstack/react-query'
import { Skeleton } from '@/components/ui/skeleton'
import { compareSupplyChainSnapshots, listSupplyChainSnapshots } from '@/api/supply-chain'
import { ErrorNotice } from '@/pages/admin/shared'
import { ANY, isDisabled, text, when } from './format'
import { idParam, useViewParams } from './params'
import { ChoiceSelect, DisabledModule, StateMessage, StreamSelect, ViewHeader } from './shared'
import { useSupplyChainRepositories } from './use-repositories'

/** Per list, as in the legacy page. */
const ITEM_LIMIT = 200
const NOTE_LIMIT = 50
/** Snapshot history offered as a base, newest first. */
const SNAPSHOT_LIMIT = 20

function Items({ heading, items }: { heading: string; items: string[] }) {
  const shown = items.slice(0, ITEM_LIMIT)
  return (
    <section className="grid gap-1">
      <h3 className="font-medium">{heading}</h3>
      {shown.length ? (
        shown.map((item, index) => (
          <p key={index} className="font-mono text-sm break-all">
            {item}
          </p>
        ))
      ) : (
        <p className="text-sm text-muted-foreground">None.</p>
      )}
    </section>
  )
}

export default function CompareView() {
  const { params, stream, update } = useViewParams()
  const repositoryId = idParam(params.get('repo'))
  const baseId = idParam(params.get('base'))
  const repositories = useSupplyChainRepositories()

  const snapshots = useQuery({
    queryKey: ['supply-chain', 'snapshots', repositoryId, stream],
    queryFn: ({ signal }) => listSupplyChainSnapshots(repositoryId as number, { stream, limit: SNAPSHOT_LIMIT }, signal),
    enabled: repositoryId !== undefined,
    retry: false,
  })
  // The newest snapshot is the head; every older one can be the base.
  const head = snapshots.data?.snapshots[0]
  const bases = (snapshots.data?.snapshots ?? []).filter((snapshot) => snapshot.id !== head?.id)

  const comparison = useQuery({
    queryKey: ['supply-chain', 'compare', repositoryId, baseId, head?.id],
    queryFn: ({ signal }) => compareSupplyChainSnapshots({ repository_id: repositoryId as number, base: baseId as number, head: head?.id as number }, signal),
    enabled: repositoryId !== undefined && baseId !== undefined && head !== undefined,
    retry: false,
  })

  if (repositories.isError && isDisabled(repositories.error)) {
    return (
      <div className="grid gap-4">
        <ViewHeader title="Snapshot comparison" />
        <DisabledModule error={repositories.error} />
      </div>
    )
  }

  const result = comparison.data
  const changes = (result?.license_changes ?? []).map((change) => `${text(change.component)}: ${text(change.from)} → ${text(change.to)}`)

  return (
    <div className="grid gap-4">
      <ViewHeader title="Snapshot comparison" />
      <div className="flex flex-wrap items-end gap-3">
        <StreamSelect stream={stream} onChange={(value) => update({ stream: value, base: undefined })} />
        <ChoiceSelect
          id="sc-repository"
          label="Repository"
          value={repositoryId === undefined ? ANY : String(repositoryId)}
          choices={[
            { value: ANY, label: 'Pick a repository' },
            ...(repositories.data ?? []).map((repository) => ({ value: String(repository.github_id), label: repository.name || String(repository.github_id) })),
          ]}
          onChange={(value) => update({ repo: value === ANY ? undefined : value, base: undefined })}
          disabled={!repositories.data}
        />
        <ChoiceSelect
          id="sc-compare-base"
          label="Compare with previous snapshot"
          value={baseId === undefined ? ANY : String(baseId)}
          choices={[
            { value: ANY, label: bases.length ? 'Pick a base snapshot' : 'Only one snapshot has been collected' },
            ...bases.map((snapshot) => ({ value: String(snapshot.id), label: `#${snapshot.id} · ${when(snapshot.collected_at)}` })),
          ]}
          onChange={(value) => update({ base: value === ANY ? undefined : value })}
          disabled={!bases.length}
        />
      </div>
      {repositories.isError && <ErrorNotice error={repositories.error} onRetry={() => void repositories.refetch()} />}
      {repositoryId === undefined && <StateMessage>Pick a repository to compare its snapshots.</StateMessage>}
      {snapshots.isPending && repositoryId !== undefined && <Skeleton className="h-10 w-64" aria-label="Loading snapshots" />}
      {snapshots.isError && <ErrorNotice error={snapshots.error} onRetry={() => void snapshots.refetch()} />}
      {comparison.isPending && comparison.fetchStatus === 'fetching' && (
        <>
          <StateMessage>Loading comparison…</StateMessage>
          <Skeleton className="h-24 w-full" aria-label="Loading comparison" />
        </>
      )}
      {comparison.isError && <ErrorNotice error={comparison.error} />}
      {result && (
        <div className="grid gap-4">
          <p>{`Base #${text(result.base?.id)} · ${when(result.base?.collected_at)} → head #${text(result.head?.id)} · ${when(result.head?.collected_at)}`}</p>
          <Items heading="Added components" items={result.added_components} />
          <Items heading="Removed components" items={result.removed_components} />
          <Items heading="License changes" items={changes} />
          <Items heading="Document metadata" items={result.metadata_changes} />
          <section className="grid gap-1">
            <h3 className="font-medium">Dependency edges</h3>
            <p className="text-sm">{`${result.edges_added || 0} added · ${result.edges_removed || 0} removed`}</p>
          </section>
          {result.notes.slice(0, NOTE_LIMIT).map((note, index) => (
            <p key={index} className="text-sm text-muted-foreground">
              {note}
            </p>
          ))}
        </div>
      )}
    </div>
  )
}
