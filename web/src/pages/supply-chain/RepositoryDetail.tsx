import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import {
  componentsCsvPath,
  derivedSpdxPath,
  downloadSupplyChainFile,
  getSupplyChainRepository,
  listSupplyChainCollections,
  listSupplyChainRepositoryComponents,
} from '@/api/supply-chain'
import { ApiError } from '@/lib/api'
import { ErrorNotice, Panel, mono } from '@/pages/admin/shared'
import { ComponentEvidenceSheet } from './ComponentEvidenceSheet'
import { PAGE_LIMIT, enrichmentText, humanize, licenseSummaryText, messageOf, text, when, yesNo } from './format'
import { useViewParams } from './params'
import { AssessmentCell, ScopePill, StatusPill } from './pill'
import { KeyValues, LoadMore, MetricCard, StateMessage } from './shared'
import type { Say } from './use-refresh'
import { useCursorList } from './use-cursor-list'

const SEARCH_DEBOUNCE_MS = 250
const NOTE_LIMIT = 50

/** Inventory of one repository: observation, warnings, components, collection history, downloads. */
export function RepositoryDetail({
  repositoryId,
  name,
  stream,
  say,
}: {
  repositoryId: number
  name: string
  stream: string
  say: Say
}) {
  const { params, update } = useViewParams()
  const q = params.get('q') ?? ''
  const element = params.get('element') ?? ''
  const evidenceSnapshot = Number(params.get('snapshot')) || undefined

  const [search, setSearch] = useState(q)
  useEffect(() => {
    if (search === q) return
    const timer = setTimeout(() => update({ q: search }), SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [search, q, update])

  const statusQuery = useQuery({
    queryKey: ['supply-chain', 'repository', repositoryId, stream],
    queryFn: ({ signal }) => getSupplyChainRepository(repositoryId, { stream }, signal),
    retry: false,
  })
  const collections = useQuery({
    queryKey: ['supply-chain', 'collections', repositoryId, stream],
    queryFn: ({ signal }) => listSupplyChainCollections(repositoryId, { stream }, signal),
    retry: false,
  })
  const list = useCursorList({
    queryKey: ['supply-chain', 'repository-components', repositoryId, stream, q],
    fetchPage: (cursor, signal) => listSupplyChainRepositoryComponents(repositoryId, { stream, q, cursor, limit: PAGE_LIMIT }, signal),
  })

  const report = statusQuery.data
  const snapshot = report?.latest_snapshot
  const reference = report?.documents[0]
  const components = list.pages.flatMap((page) => page.components)
  const snapshotId = list.pages[0]?.snapshot_id
  const noInventory = list.query.error instanceof ApiError && list.query.error.body?.error.code === 'no_inventory'

  async function download(path: string, fallbackName: string, done: string) {
    try {
      await downloadSupplyChainFile(path, fallbackName)
      say(done)
    } catch (failure) {
      if (!(failure instanceof ApiError && failure.status === 401)) say(messageOf(failure), true)
    }
  }

  return (
    <section aria-label={`Inventory of ${name}`} className="grid gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="mr-auto font-mono text-base font-medium break-all">{name}</h3>
        <Button
          type="button"
          variant="outline"
          disabled={!reference}
          onClick={() => reference && void download(reference.path, `graphnest-sbom-snapshot${reference.snapshot_id}.json`, 'Original document downloaded.')}
        >
          <Download /> Download original document
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={!snapshotId}
          onClick={() => snapshotId && void download(componentsCsvPath(repositoryId, { stream, snapshot_id: snapshotId }), `graphnest-components-${snapshotId}.csv`, 'Component CSV downloaded.')}
        >
          <Download /> Download CSV
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={!snapshotId}
          onClick={() => snapshotId && void download(derivedSpdxPath(repositoryId, { stream, snapshot_id: snapshotId }), `graphnest-derived-snapshot${snapshotId}.spdx.json`, 'Derived SPDX document downloaded.')}
        >
          <Download /> Download derived SPDX
        </Button>
        <Button asChild variant="outline">
          <Link to={`/supply-chain/compare?${new URLSearchParams({ repo: String(repositoryId), stream })}`}>Compare snapshots</Link>
        </Button>
      </div>

      {statusQuery.isPending && <Skeleton className="h-24 w-full" aria-label="Loading inventory status" />}
      {statusQuery.isError && <ErrorNotice error={statusQuery.error} onRetry={() => void statusQuery.refetch()} />}
      {report && (
        <>
          {report.notes.slice(0, NOTE_LIMIT).map((note, index) => (
            <p key={index} className="text-sm text-muted-foreground">
              {note}
            </p>
          ))}
          {report.collection === 'failed' && snapshot && (
            <p role="status" className="rounded-md border border-amber-600/40 p-3 text-sm">
              <span>The inventory shown is the last successful observation; the most recent refresh failed.</span>
              {report.last_collection && (
                <span className="block">
                  {`Last attempt: ${text(report.last_collection.outcome)} · HTTP ${text(report.last_collection.http_status)} · ${text(report.last_collection.message)}`}
                </span>
              )}
            </p>
          )}
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-5" role="group" aria-label="Inventory summary">
            <MetricCard label="Collection state" value={<StatusPill value={report.collection} label="Collection state" />} denominator="state of the latest attempt" />
            <MetricCard label="Freshness" value={humanize(report.freshness_seconds)} denominator="age of the latest successful observation" />
            <MetricCard label="Components" value={snapshot ? String(snapshot.component_count || 0) : '—'} denominator="in the latest snapshot" />
            <MetricCard label="Warnings" value={snapshot ? String(snapshot.warning_count || 0) : '0'} denominator="parser warnings in the latest snapshot" />
            <MetricCard label="Licenses" value={licenseSummaryText(report.license_summary)} denominator="assessments in the latest snapshot" />
          </div>
          <div className="grid gap-4 lg:grid-cols-2">
            <Panel title="Observation">
              <KeyValues
                rows={[
                  ['Subject assurance', 'unknown — not bound to a commit'],
                  ['Producer', text(report.producer)],
                  ['Producer tool', text(snapshot?.producer_tool)],
                  ['Observed by GraphNest (collected_at)', when(snapshot?.collected_at)],
                  ['Producer-claimed creation time', when(snapshot?.created_at_claimed)],
                  ['License enrichment', enrichmentText(report.enrichment, report.enrichment_ecosystems)],
                  ['Snapshot', snapshot ? `#${snapshot.id}` : '—'],
                ]}
              />
              <p className="font-mono break-all">
                {reference ? `sha256 ${reference.sha256} · ${reference.format} · ${reference.bytes} bytes` : 'No original document is stored.'}
              </p>
            </Panel>
            <Panel title={`Warnings (${snapshot?.warnings?.length ?? 0})`}>
              {snapshot?.warnings?.length ? (
                snapshot.warnings.map((warning, index) => <p key={index}>{`${text(warning.code)} · ${text(warning.element)} · ${text(warning.detail)}`}</p>)
              ) : (
                <p className="text-muted-foreground">No parser warnings were recorded.</p>
              )}
            </Panel>
          </div>
        </>
      )}

      <div className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1.5">
          <Label htmlFor="sc-component-search">Search components</Label>
          <Input id="sc-component-search" type="search" className="w-64" placeholder="Search components" maxLength={200} value={search} onChange={(event) => setSearch(event.target.value)} />
        </div>
        <p className="pb-2 text-sm text-muted-foreground">{components.length ? `Showing ${components.length} of snapshot #${snapshotId}` : ''}</p>
      </div>
      <div aria-live="polite">
        {list.query.isPending && <StateMessage>Loading components…</StateMessage>}
        {list.query.isSuccess && components.length === 0 && <StateMessage>No components in this snapshot</StateMessage>}
        {noInventory && <StateMessage>No inventory has been collected yet</StateMessage>}
      </div>
      {list.query.isError && !noInventory && <ErrorNotice error={list.query.error} onRetry={() => void list.query.refetch()} />}
      {list.query.isPending ? (
        <Skeleton className="h-32 w-full" aria-label="Loading components" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              {['Name', 'Version', 'Ecosystem', 'PURL', 'Scope', 'Declared license', 'Assessed license', 'Root'].map((heading) => (
                <TableHead key={heading} scope="col">
                  {heading}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {components.map((component) => (
              <TableRow key={`${component.ordinal}:${component.element_id}`}>
                <TableCell className="whitespace-normal">
                  <Button type="button" variant="link" className="h-auto p-0 text-left break-all whitespace-normal" onClick={() => update({ element: component.element_id, snapshot: undefined })}>
                    {text(component.name)}
                  </Button>
                </TableCell>
                <TableCell>{text(component.version)}</TableCell>
                <TableCell>{text(component.ecosystem)}</TableCell>
                <TableCell className={mono}>{text(component.purl)}</TableCell>
                <TableCell>
                  <ScopePill value={component.scope || 'unknown'} />
                </TableCell>
                <TableCell className={mono}>{text(component.license_declared_raw)}</TableCell>
                <TableCell>
                  <AssessmentCell status={component.license?.status} expression={component.license?.expression} label="Assessed license" />
                </TableCell>
                <TableCell>{yesNo(component.is_root)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {list.query.isError && components.length > 0 && <ErrorNotice error={list.query.error} />}
      <LoadMore visible={list.hasMore} loading={list.loadingMore} onClick={() => void list.loadMore()} />

      <Panel title="Recent collection attempts">
        {collections.isPending && <Skeleton className="h-16 w-full" aria-label="Loading collection attempts" />}
        {collections.isError && <ErrorNotice error={collections.error} />}
        {collections.data && (
          <Table>
            <TableHeader>
              <TableRow>
                {['Finished', 'Outcome', 'HTTP status', 'Error code', 'Message', 'Projection error'].map((heading) => (
                  <TableHead key={heading} scope="col">
                    {heading}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {collections.data.collections.map((collection) => (
                <TableRow key={collection.id}>
                  <TableCell>{when(collection.finished_at)}</TableCell>
                  <TableCell>
                    <StatusPill value={collection.outcome} label="Outcome" />
                  </TableCell>
                  <TableCell>{text(collection.http_status)}</TableCell>
                  <TableCell>{text(collection.error_code)}</TableCell>
                  <TableCell className="whitespace-normal">{text(collection.message)}</TableCell>
                  <TableCell className="whitespace-normal">{text(collection.projection_error)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>

      <ComponentEvidenceSheet
        repositoryId={repositoryId}
        element={element}
        snapshotId={evidenceSnapshot ?? snapshotId}
        stream={stream}
        onClose={() => update({ element: undefined, snapshot: undefined })}
      />
    </section>
  )
}
