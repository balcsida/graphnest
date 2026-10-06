import { graphlib, layout as dagreLayout } from '@dagrejs/dagre'
import type { Edge, Node } from '@xyflow/react'
import type { SupplyChainPortfolioComponent, SupplyChainPortfolioComponentList } from '@/api/types'
import { packageLabel } from '../format'

export const SCAN_PAGE_SIZE = 100
export const SCAN_PAGE_CAP = 30
export const DEFAULT_MIN_REPOSITORIES = 2
export const MAX_DEPENDENCY_NODES = 150

export interface ScanResult {
  components: SupplyChainPortfolioComponent[]
  pages: number
  /** The scan stopped at the page cap while the server still had more. */
  capped: boolean
}

/** Reads component pages until the server has no more or the cap is reached. `signal` cancels the chain. */
export async function scanComponents(
  fetchPage: (cursor: string | undefined, signal: AbortSignal | undefined) => Promise<SupplyChainPortfolioComponentList>,
  signal?: AbortSignal,
  pageCap = SCAN_PAGE_CAP,
): Promise<ScanResult> {
  const components: SupplyChainPortfolioComponent[] = []
  let cursor: string | undefined
  let pages = 0
  let more: boolean
  do {
    const page = await fetchPage(cursor, signal)
    pages++
    components.push(...(Array.isArray(page.components) ? page.components : []))
    more = page.truncated && Boolean(page.next_cursor)
    cursor = more ? page.next_cursor : undefined
  } while (more && pages < pageCap)
  return { components, pages, capped: more }
}

/** "based on the first N of M unique coordinates"; only when the scan was cut off. */
export const truncationNotice = (scan: Pick<ScanResult, 'components' | 'capped'>, uniqueCoordinates: number | undefined) =>
  scan.capped ? `Based on the first ${scan.components.length} of ${uniqueCoordinates ?? 'an unknown number of'} unique coordinates; narrow the filters to see the rest.` : undefined

export interface GraphFilters {
  /** Dependencies used by fewer repositories are left out. */
  minRepositories: number
  /** At most this many dependency nodes, most-shared first. */
  topN: number
  ecosystem?: string
  assessment?: string
  q?: string
}

export interface GraphRepository {
  id: number
  name: string
  dependencyIds: string[]
}

export interface GraphDependency {
  /** ecosystem, namespace and name; one node per package across versions. */
  id: string
  ecosystem: string
  namespace?: string
  name: string
  label: string
  versions: string[]
  componentKeys: string[]
  expressions: string[]
  assessments: string[]
  /** Any occurrence is a conflict or unlicensed. */
  risky: boolean
  repositoryIds: number[]
}

export interface DependencyGraph {
  repositories: GraphRepository[]
  dependencies: GraphDependency[]
  edges: { repositoryId: number; dependencyId: string }[]
  /** Dependencies that passed the filters and the minimum, before the top-N cut. */
  matching: number
  /** Distinct packages in the scan, before any filter. */
  packages: number
}

const sorted = (values: Iterable<string>) => [...new Set(values)].filter(Boolean).sort((a, b) => a.localeCompare(b, undefined, { numeric: true }))

const RISKY = new Set(['conflict', 'unlicensed'])

const matches = (component: SupplyChainPortfolioComponent, filters: GraphFilters) => {
  if (filters.ecosystem && component.ecosystem !== filters.ecosystem) return false
  if (filters.assessment && component.assessment !== filters.assessment && !component.assessment_statuses?.includes(filters.assessment)) return false
  const q = filters.q?.trim().toLowerCase()
  return !q || component.name.toLowerCase().includes(q) || (component.purl ?? '').toLowerCase().includes(q)
}

/**
 * Groups the scanned coordinates into packages (across versions), applies the filters and the minimum shared
 * repositories, keeps the `topN` most shared packages and links each to the repositories using it.
 * `repositoryNames` fills in names the component pages do not carry.
 */
export function buildGraph(components: SupplyChainPortfolioComponent[], filters: GraphFilters, repositoryNames: ReadonlyMap<number, string> = new Map()): DependencyGraph {
  const all = new Map<string, SupplyChainPortfolioComponent[]>()
  const reposById = new Map<number, string>()
  for (const component of components) {
    const id = [component.ecosystem, component.namespace ?? '', component.name].join('\u0000')
    const group = all.get(id)
    if (group) group.push(component)
    else all.set(id, [component])
  }

  const groups: GraphDependency[] = []
  for (const [id, group] of all) {
    const kept = group.filter((component) => matches(component, filters))
    if (kept.length === 0) continue
    const repositoryIds = new Set<number>()
    for (const component of kept) {
      for (const repository of component.repositories ?? []) {
        repositoryIds.add(repository.id)
        const name = repository.name || repositoryNames.get(repository.id)
        if (name) reposById.set(repository.id, name)
      }
    }
    const [first] = kept
    const assessments = sorted(kept.flatMap((component) => component.assessment_statuses?.length ? component.assessment_statuses : [component.assessment]))
    groups.push({
      id,
      ecosystem: first.ecosystem,
      namespace: first.namespace,
      name: first.name,
      label: packageLabel(first.namespace, first.name),
      versions: sorted(kept.map((component) => component.version)),
      componentKeys: kept.map((component) => component.key),
      expressions: sorted(kept.map((component) => component.expression ?? '')),
      assessments,
      risky: assessments.some((status) => RISKY.has(status)),
      repositoryIds: [...repositoryIds].sort((a, b) => a - b),
    })
  }

  const shared = groups
    .filter((group) => group.repositoryIds.length >= filters.minRepositories)
    .sort((a, b) => b.repositoryIds.length - a.repositoryIds.length || a.label.localeCompare(b.label) || a.ecosystem.localeCompare(b.ecosystem))
  const dependencies = shared.slice(0, filters.topN)

  const byRepository = new Map<number, string[]>()
  const edges: DependencyGraph['edges'] = []
  for (const dependency of dependencies) {
    for (const repositoryId of dependency.repositoryIds) {
      byRepository.set(repositoryId, [...(byRepository.get(repositoryId) ?? []), dependency.id])
      edges.push({ repositoryId, dependencyId: dependency.id })
    }
  }
  const repositories = [...byRepository]
    .map(([id, dependencyIds]) => ({ id, name: reposById.get(id) ?? repositoryNames.get(id) ?? `Repository ${id}`, dependencyIds }))
    .sort((a, b) => a.name.localeCompare(b.name) || a.id - b.id)

  return { repositories, dependencies, edges, matching: shared.length, packages: all.size }
}

export const repositoryNodeId = (id: number) => `r:${id}`
export const dependencyNodeId = (id: string) => `d:${id}`

export type RepositoryNodeData = { kind: 'repository'; repository: GraphRepository; dimmed: boolean }
export type DependencyNodeData = { kind: 'dependency'; dependency: GraphDependency; colorIndex: number; dimmed: boolean }
export type RepositoryFlowNode = Node<RepositoryNodeData, 'repository'>
export type DependencyFlowNode = Node<DependencyNodeData, 'dependency'>
export type GraphFlowNode = RepositoryFlowNode | DependencyFlowNode

export const REPOSITORY_NODE = { width: 220, height: 44 }
export const DEPENDENCY_NODE = { width: 240, height: 48 }

/** Theme chart colour of an ecosystem slot, so dark mode follows the theme. */
export const ecosystemColor = (index: number) => `var(--chart-${(index % 5) + 1})`

/** Stable colour slot per ecosystem, in alphabetical order. */
export const ecosystemColorIndexes = (dependencies: GraphDependency[]) => new Map(sorted(dependencies.map((dependency) => dependency.ecosystem)).map((ecosystem, index) => [ecosystem, index]))

/** Dagre left-to-right layout: repositories on the left, their dependencies on the right. */
export function layoutGraph(graph: DependencyGraph): { nodes: GraphFlowNode[]; edges: Edge[] } {
  const layout = new graphlib.Graph()
  layout.setGraph({ rankdir: 'LR', nodesep: 10, ranksep: 160, marginx: 16, marginy: 16 })
  layout.setDefaultEdgeLabel(() => ({}))
  for (const repository of graph.repositories) layout.setNode(repositoryNodeId(repository.id), { ...REPOSITORY_NODE })
  for (const dependency of graph.dependencies) layout.setNode(dependencyNodeId(dependency.id), { ...DEPENDENCY_NODE })
  for (const edge of graph.edges) layout.setEdge(repositoryNodeId(edge.repositoryId), dependencyNodeId(edge.dependencyId))
  dagreLayout(layout)

  const colors = ecosystemColorIndexes(graph.dependencies)
  // Dagre writes x/y into the label object it is given, so each node gets its own copy above.
  // Dagre positions the centre of a node; React Flow wants the top-left corner.
  const place = (id: string, size: { width: number; height: number }) => {
    const { x, y } = layout.node(id)
    return { x: x - size.width / 2, y: y - size.height / 2 }
  }
  const nodes: GraphFlowNode[] = [
    ...graph.repositories.map((repository): RepositoryFlowNode => ({
      id: repositoryNodeId(repository.id),
      type: 'repository',
      ariaLabel: `Repository ${repository.name}`,
      position: place(repositoryNodeId(repository.id), REPOSITORY_NODE),
      data: { kind: 'repository', repository, dimmed: false },
      ...REPOSITORY_NODE,
    })),
    ...graph.dependencies.map((dependency): DependencyFlowNode => ({
      id: dependencyNodeId(dependency.id),
      type: 'dependency',
      ariaLabel: `Dependency ${dependency.ecosystem} ${dependency.label}`,
      position: place(dependencyNodeId(dependency.id), DEPENDENCY_NODE),
      data: { kind: 'dependency', dependency, colorIndex: colors.get(dependency.ecosystem) ?? 0, dimmed: false },
      ...DEPENDENCY_NODE,
    })),
  ]
  const edges: Edge[] = graph.edges.map((edge) => ({
    id: `${repositoryNodeId(edge.repositoryId)}->${dependencyNodeId(edge.dependencyId)}`,
    source: repositoryNodeId(edge.repositoryId),
    target: dependencyNodeId(edge.dependencyId),
  }))
  return { nodes, edges }
}
