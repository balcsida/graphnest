import { graphlib, layout as dagreLayout } from '@dagrejs/dagre'
import type { Edge, Node } from '@xyflow/react'
import type { SupplyChainDependencyGraph } from '@/api/types'
import { packageLabel } from '../format'

export const DEFAULT_MIN_REPOSITORIES = 2
export const MAX_DEPENDENCY_NODES = 150

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
  /** The first of `versionCount` versions. */
  versions: string[]
  versionCount: number
  expressions: string[]
  assessments: string[]
  /** Any occurrence is a conflict or unlicensed. */
  risky: boolean
  /** Repositories using it; `repositoryIds` are the ones the graph links. */
  repositoryCount: number
  repositoryIds: number[]
}

export interface DependencyGraph {
  repositories: GraphRepository[]
  dependencies: GraphDependency[]
  edges: { repositoryId: number; dependencyId: string }[]
  /** Dependencies that passed the filters and the minimum, before the limit. */
  matching: number
  /** The server left some repositories of a dependency out to keep the graph within its link budget. */
  partial: boolean
}

const sorted = (values: Iterable<string>) => [...new Set(values)].filter(Boolean).sort((a, b) => a.localeCompare(b, undefined, { numeric: true }))

const RISKY = new Set(['conflict', 'unlicensed'])

/** "1.0.0, 1.1.0 and 3 more": the listed versions and how many the server left out. */
export const versionsText = (dependency: Pick<GraphDependency, 'versions' | 'versionCount'>) => {
  const more = dependency.versionCount - dependency.versions.length
  return `${dependency.versions.join(', ')}${more > 0 ? ` and ${more} more` : ''}`
}

/** Turns the server's graph into nodes and edges: one node per dependency and per repository a dependency lists. */
export function buildGraph(response: SupplyChainDependencyGraph | undefined): DependencyGraph {
  const dependencies = (response?.dependencies ?? [])
    .map((dependency): GraphDependency => {
      const assessments = sorted(dependency.assessments)
      return {
        id: [dependency.ecosystem, dependency.namespace ?? '', dependency.name].join('\u0000'),
        ecosystem: dependency.ecosystem,
        namespace: dependency.namespace,
        name: dependency.name,
        label: packageLabel(dependency.namespace, dependency.name),
        versions: sorted(dependency.versions),
        versionCount: dependency.version_count,
        expressions: sorted(dependency.expressions),
        assessments,
        risky: assessments.some((status) => RISKY.has(status)),
        repositoryCount: dependency.repository_count,
        repositoryIds: dependency.repository_ids,
      }
    })
    .sort((a, b) => b.repositoryCount - a.repositoryCount || a.label.localeCompare(b.label) || a.ecosystem.localeCompare(b.ecosystem))

  const names = new Map((response?.repositories ?? []).map((repository) => [repository.id, repository.name]))
  const byRepository = new Map<number, string[]>()
  const edges: DependencyGraph['edges'] = []
  for (const dependency of dependencies) {
    for (const repositoryId of dependency.repositoryIds) {
      byRepository.set(repositoryId, [...(byRepository.get(repositoryId) ?? []), dependency.id])
      edges.push({ repositoryId, dependencyId: dependency.id })
    }
  }
  const repositories = [...byRepository]
    .map(([id, dependencyIds]) => ({ id, name: names.get(id) || `Repository ${id}`, dependencyIds }))
    .sort((a, b) => a.name.localeCompare(b.name) || a.id - b.id)

  return {
    repositories,
    dependencies,
    edges,
    matching: response?.matching ?? 0,
    partial: dependencies.some((dependency) => dependency.repositoryIds.length < dependency.repositoryCount),
  }
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
