import '@xyflow/react/dist/style.css'
import { useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Background, Controls, MiniMap, ReactFlow, type NodeTypes } from '@xyflow/react'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { getSupplyChainFacets, getSupplyChainOverview, listSupplyChainComponents } from '@/api/supply-chain'
import type { SupplyChainFacet } from '@/api/types'
import { ErrorNotice } from '@/pages/admin/shared'
import { ANY, facetLabel, isDisabled } from '../format'
import { useViewParams } from '../params'
import { ChoiceSelect, DisabledModule, StateMessage, StreamSelect, ViewHeader, type Choice } from '../shared'
import { useSupplyChainRepositories } from '../use-repositories'
import {
  DEFAULT_MIN_REPOSITORIES,
  MAX_DEPENDENCY_NODES,
  SCAN_PAGE_SIZE,
  buildGraph,
  dependencyNodeId,
  ecosystemColor,
  ecosystemColorIndexes,
  layoutGraph,
  repositoryNodeId,
  scanComponents,
  truncationNotice,
  type GraphFlowNode,
} from './build-graph'
import { EdgeTable } from './EdgeTable'
import { GraphSheet, type Selection } from './GraphSheet'
import { DependencyNode, RepositoryNode } from './nodes'

const nodeTypes: NodeTypes = { repository: RepositoryNode, dependency: DependencyNode }

const MIN_CHOICES: Choice[] = [1, 2, 3, 5, 10].map((value) => ({ value: String(value), label: `At least ${value} ${value === 1 ? 'repository' : 'repositories'}` }))
const TOP_CHOICES: Choice[] = [25, 50, 100, MAX_DEPENDENCY_NODES].map((value) => ({ value: String(value), label: `Top ${value} dependencies` }))
const SEARCH_DEBOUNCE_MS = 250

function facetChoices(allLabel: string, facets: SupplyChainFacet[] | undefined, current: string): Choice[] {
  const choices = [{ value: ANY, label: allLabel }, ...(facets ?? []).map((facet) => ({ value: facet.value, label: facetLabel(facet.value, facet.count) }))]
  if (current && !choices.some((choice) => choice.value === current)) choices.push({ value: current, label: current })
  return choices
}

const choiceParam = (value: string | null, choices: Choice[], fallback: number) => (choices.some((choice) => choice.value === value) ? Number(value) : fallback)

/** The shell toggles `.dark` on <html>; React Flow needs to be told because its chrome is not themed by our CSS variables. */
function subscribeToTheme(onChange: () => void) {
  const observer = new MutationObserver(onChange)
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
  return () => observer.disconnect()
}
const isDark = () => document.documentElement.classList.contains('dark')

export default function GraphView() {
  const { params, stream, update } = useViewParams()
  const ecosystem = params.get('ecosystem') ?? ''
  const assessment = params.get('assessment') ?? ''
  const q = params.get('q') ?? ''
  const minRepositories = choiceParam(params.get('min'), MIN_CHOICES, DEFAULT_MIN_REPOSITORIES)
  const topN = choiceParam(params.get('top'), TOP_CHOICES, MAX_DEPENDENCY_NODES)

  const [search, setSearch] = useState(q)
  useEffect(() => {
    if (search === q) return
    const timer = setTimeout(() => update({ q: search }), SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [search, q, update])

  const [asTable, setAsTable] = useState(false)
  const [selection, setSelection] = useState<Selection>()
  const [hovered, setHovered] = useState<string>()
  const dark = useSyncExternalStore(subscribeToTheme, isDark)

  const overview = useQuery({
    queryKey: ['supply-chain', 'overview', stream, []],
    queryFn: ({ signal }) => getSupplyChainOverview({ stream }, signal),
    retry: false,
  })
  const facets = useQuery({
    queryKey: ['supply-chain', 'facets', stream],
    queryFn: ({ signal }) => getSupplyChainFacets({ stream }, signal),
    retry: false,
  })
  const repositories = useSupplyChainRepositories()
  const scan = useQuery({
    queryKey: ['supply-chain', 'graph-scan', stream, { ecosystem, assessment, q }],
    queryFn: ({ signal }) =>
      scanComponents((cursor, pageSignal) => listSupplyChainComponents({ stream, ecosystem, assessment, q, cursor, limit: SCAN_PAGE_SIZE }, pageSignal), signal),
    retry: false,
    refetchOnWindowFocus: false,
  })

  const graph = useMemo(() => {
    const names = new Map((repositories.data ?? []).map((repository) => [repository.github_id, repository.name]))
    return buildGraph(scan.data?.components ?? [], { minRepositories, topN, ecosystem, assessment, q }, names)
  }, [scan.data, repositories.data, minRepositories, topN, ecosystem, assessment, q])
  const layout = useMemo(() => layoutGraph(graph), [graph])

  const activeId = hovered ?? (selection ? (selection.kind === 'repository' ? repositoryNodeId(selection.id) : dependencyNodeId(selection.id)) : undefined)
  const { nodes, edges } = useMemo(() => {
    if (!activeId) return layout
    const adjacent = new Set<string>([activeId])
    for (const edge of layout.edges) {
      if (edge.source === activeId) adjacent.add(edge.target)
      if (edge.target === activeId) adjacent.add(edge.source)
    }
    const dim = (node: GraphFlowNode): GraphFlowNode => {
      const dimmed = !adjacent.has(node.id)
      return node.type === 'repository' ? { ...node, data: { ...node.data, dimmed } } : { ...node, data: { ...node.data, dimmed } }
    }
    return {
      nodes: layout.nodes.map(dim),
      edges: layout.edges.map((edge) => {
        const related = edge.source === activeId || edge.target === activeId
        return { ...edge, style: { stroke: related ? 'var(--foreground)' : 'var(--muted-foreground)', strokeWidth: related ? 2 : 1, opacity: related ? 1 : 0.08 } }
      }),
    }
  }, [layout, activeId])

  const legend = useMemo(() => [...ecosystemColorIndexes(graph.dependencies)], [graph.dependencies])
  const disabled = [overview.error, facets.error, scan.error].find(isDisabled)
  const notice = scan.data ? truncationNotice(scan.data, overview.data?.components.unique_coordinates) : undefined
  const graphKey = [stream, ecosystem, assessment, q, minRepositories, topN, layout.nodes.length, layout.edges.length].join('|')

  return (
    <div className="grid gap-4">
      <ViewHeader title="Dependency graph" />
      <p className="text-sm text-muted-foreground">Which onboarded repositories share which external dependencies. Assessments describe evidence, not compliance.</p>
      <div className="flex flex-wrap items-end gap-3">
        <StreamSelect stream={stream} onChange={(value) => update({ stream: value })} />
        <ChoiceSelect id="sc-graph-min" label="Minimum shared repositories" value={String(minRepositories)} choices={MIN_CHOICES} onChange={(value) => update({ min: value })} className="w-56" />
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
        <div className="grid gap-1.5">
          <Label htmlFor="sc-search">Search name or purl</Label>
          <Input id="sc-search" type="search" className="w-56" placeholder="Search name or purl" value={search} maxLength={200} onChange={(event) => setSearch(event.target.value)} />
        </div>
        <ChoiceSelect id="sc-graph-top" label="Dependency limit" value={String(topN)} choices={TOP_CHOICES} onChange={(value) => update({ top: value })} className="w-52" />
        <div className="flex items-center gap-2 pb-2">
          <Switch id="sc-graph-table" checked={asTable} onCheckedChange={setAsTable} />
          <Label htmlFor="sc-graph-table">Show as table</Label>
        </div>
      </div>
      {facets.isError && !isDisabled(facets.error) && <ErrorNotice error={facets.error} />}
      {disabled ? (
        <DisabledModule error={disabled} />
      ) : (
        <>
          <div aria-live="polite">
            {scan.isPending && <StateMessage>Reading components…</StateMessage>}
            {scan.isSuccess && graph.dependencies.length === 0 && <StateMessage>No shared dependencies match these filters</StateMessage>}
            {scan.isSuccess && graph.dependencies.length > 0 && (
              <StateMessage>
                Showing {graph.dependencies.length} of {graph.matching} matching dependencies used by {graph.repositories.length} repositories.
              </StateMessage>
            )}
          </div>
          {notice && (
            <Alert>
              <AlertDescription>{notice}</AlertDescription>
            </Alert>
          )}
          {scan.isError && <ErrorNotice error={scan.error} onRetry={() => void scan.refetch()} />}
          {scan.isPending && <Skeleton className="h-96 w-full" aria-label="Loading dependency graph" />}
          {scan.isSuccess &&
            (asTable ? (
              <EdgeTable graph={graph} onSelect={setSelection} />
            ) : (
              <>
                <div role="region" aria-label="Dependency graph" className="h-[65vh] min-h-96 rounded-md border">
                  <ReactFlow
                    key={graphKey}
                    nodes={nodes}
                    edges={edges}
                    nodeTypes={nodeTypes}
                    colorMode={dark ? 'dark' : 'light'}
                    fitView
                    minZoom={0.05}
                    nodesDraggable={false}
                    nodesConnectable={false}
                    onNodeClick={(_, node) => setSelection(node.data.kind === 'repository' ? { kind: 'repository', id: node.data.repository.id } : { kind: 'dependency', id: node.data.dependency.id })}
                    onNodeMouseEnter={(_, node) => setHovered(node.id)}
                    onNodeMouseLeave={() => setHovered(undefined)}
                    onPaneClick={() => setSelection(undefined)}
                  >
                    <Background />
                    <Controls showInteractive={false} />
                    <MiniMap pannable zoomable ariaLabel="Dependency graph minimap" />
                  </ReactFlow>
                </div>
                <div role="group" aria-label="Legend" className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
                  <span className="inline-flex items-center gap-1.5">
                    <span aria-hidden="true" className="inline-block h-3 w-5 rounded-sm border bg-card" />
                    Repository (left)
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span aria-hidden="true" className="inline-block h-3 w-5 rounded-sm border-2 border-destructive bg-card" />
                    Dependency with a conflict or unlicensed occurrence
                  </span>
                  {legend.map(([name, index]) => (
                    <span key={name} className="inline-flex items-center gap-1.5">
                      <span aria-hidden="true" className="inline-block size-3 rounded-sm" style={{ background: ecosystemColor(index) }} />
                      {name}
                    </span>
                  ))}
                  <span>Hover or select a node to highlight its edges.</span>
                </div>
              </>
            ))}
          <GraphSheet selection={selection} graph={graph} stream={stream} onClose={() => setSelection(undefined)} />
        </>
      )}
    </div>
  )
}
