import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { getSupplyChainFacets, listSupplyChainComponents } from '@/api/supply-chain'
import type { SupplyChainFacet, SupplyChainPortfolioOccurrence } from '@/api/types'
import { ErrorNotice, mono } from '@/pages/admin/shared'
import { PortfolioDetailSheet } from './PortfolioDetailSheet'
import { ANY, PAGE_LIMIT, declaredText, facetLabel, isDisabled, packageLabel, since, text } from './format'
import { useViewParams } from './params'
import { AssessmentCell } from './pill'
import { ChoiceSelect, DisabledModule, LoadMore, StateMessage, StreamSelect, ViewHeader, type Choice } from './shared'
import { useCursorList } from './use-cursor-list'

const SEARCH_DEBOUNCE_MS = 250

function facetChoices(allLabel: string, facets: SupplyChainFacet[] | undefined, current: string): Choice[] {
  const choices = [{ value: ANY, label: allLabel }, ...(facets ?? []).map((facet) => ({ value: facet.value, label: facetLabel(facet.value, facet.count) }))]
  // A value from the URL that the facets do not list stays visible.
  if (current && !choices.some((choice) => choice.value === current)) choices.push({ value: current, label: current })
  return choices
}

export default function ComponentsView() {
  const { params, stream, update } = useViewParams()
  const navigate = useNavigate()
  const ecosystem = params.get('ecosystem') ?? ''
  const assessment = params.get('assessment') ?? ''
  const license = params.get('license') ?? ''
  const q = params.get('q') ?? ''
  const detailKey = params.get('key') ?? ''

  const [search, setSearch] = useState(q)
  useEffect(() => {
    if (search === q) return
    const timer = setTimeout(() => update({ q: search }), SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [search, q, update])

  const facets = useQuery({
    queryKey: ['supply-chain', 'facets', stream],
    queryFn: ({ signal }) => getSupplyChainFacets({ stream }, signal),
    retry: false,
  })
  const list = useCursorList({
    queryKey: ['supply-chain', 'components', stream, { ecosystem, assessment, license, q }],
    fetchPage: (cursor, signal) =>
      listSupplyChainComponents({ stream, ecosystem, assessment, license, q, cursor, limit: PAGE_LIMIT }, signal),
  })
  const components = list.pages.flatMap((page) => page.components)
  const inScope = list.pages[0]?.repositories_in_scope
  const disabled = [facets.error, list.query.error].find(isDisabled)
  const firstError = list.query.isError && components.length === 0

  function openEvidence(occurrence: SupplyChainPortfolioOccurrence) {
    const next = new URLSearchParams({ repo: String(occurrence.repository_id), stream, element: occurrence.element_id })
    if (occurrence.snapshot_id) next.set('snapshot', String(occurrence.snapshot_id))
    void navigate(`/supply-chain/repositories?${next.toString()}`)
  }

  if (disabled) {
    return (
      <div className="grid gap-4">
        <ViewHeader title="Components" />
        <DisabledModule error={disabled} />
      </div>
    )
  }

  return (
    <div className="grid gap-4">
      <ViewHeader title="Components" />
      <div className="flex flex-wrap items-end gap-3">
        <StreamSelect stream={stream} onChange={(value) => update({ stream: value })} />
        <ChoiceSelect
          id="sc-ecosystem"
          label="Ecosystem"
          value={ecosystem || ANY}
          choices={facetChoices('All ecosystems', facets.data?.ecosystems, ecosystem)}
          onChange={(value) => update({ ecosystem: value === ANY ? undefined : value })}
          className="w-48"
        />
        <ChoiceSelect
          id="sc-assessment"
          label="Assessment"
          value={assessment || ANY}
          choices={facetChoices('All assessments', facets.data?.assessments, assessment)}
          onChange={(value) => update({ assessment: value === ANY ? undefined : value })}
          className="w-48"
        />
        <ChoiceSelect
          id="sc-license"
          label="License expression"
          value={license || ANY}
          choices={facetChoices('All license expressions', facets.data?.licenses, license)}
          onChange={(value) => update({ license: value === ANY ? undefined : value })}
          className="w-64"
        />
        <div className="grid gap-1.5">
          <Label htmlFor="sc-search">Search name or purl</Label>
          <Input id="sc-search" type="search" className="w-56" placeholder="Search name or purl" value={search} maxLength={200} onChange={(event) => setSearch(event.target.value)} />
        </div>
        {inScope !== undefined && <p className="pb-2 text-sm text-muted-foreground">{inScope} repositories in scope</p>}
      </div>
      {facets.isError && <ErrorNotice error={facets.error} />}
      <div aria-live="polite">
        {list.query.isPending && <StateMessage>Loading components…</StateMessage>}
        {list.query.isSuccess && components.length === 0 && <StateMessage>No components match these filters</StateMessage>}
      </div>
      {firstError && <ErrorNotice error={list.query.error} onRetry={() => void list.query.refetch()} />}
      {list.query.isPending ? (
        <Skeleton className="h-32 w-full" aria-label="Loading components" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              {['Package', 'Version', 'Ecosystem', 'Assessment', 'Repositories', 'Occurrences', 'Declared', 'Newest observation'].map((heading) => (
                <TableHead key={heading} scope="col">
                  {heading}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {components.map((component) => (
              <TableRow key={component.key}>
                <TableCell className="whitespace-normal">
                  <Button type="button" variant="link" className="h-auto p-0 text-left break-all whitespace-normal" onClick={() => update({ key: component.key })}>
                    {packageLabel(component.namespace, component.name)}
                  </Button>
                </TableCell>
                <TableCell>{text(component.version)}</TableCell>
                <TableCell>{text(component.ecosystem)}</TableCell>
                <TableCell>
                  <AssessmentCell status={component.assessment || 'unassessed'} expression={component.expression} label="Assessment" />
                </TableCell>
                <TableCell>{component.repository_count || 0}</TableCell>
                <TableCell>{component.occurrence_count || 0}</TableCell>
                <TableCell className={mono}>{declaredText(component.declared_raw)}</TableCell>
                <TableCell>{since(component.newest_collected_at)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {list.query.isError && components.length > 0 && <ErrorNotice error={list.query.error} />}
      <LoadMore visible={list.hasMore} loading={list.loadingMore} onClick={() => void list.loadMore()} />
      <PortfolioDetailSheet
        componentKey={detailKey}
        stream={stream}
        purl={components.find((component) => component.key === detailKey)?.purl}
        onClose={() => update({ key: undefined })}
        onEvidence={openEvidence}
      />
    </div>
  )
}
