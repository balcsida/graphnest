import { describe, expect, it } from 'vitest'
import type { SupplyChainDependencyGraph, SupplyChainGraphDependency } from '@/api/types'
import { buildGraph, dependencyNodeId, layoutGraph, repositoryNodeId, versionsText } from './build-graph'

const dependency = (name: string, repositories: number[], extra: Partial<SupplyChainGraphDependency> = {}): SupplyChainGraphDependency => ({
  ecosystem: 'npm',
  name,
  version_count: 1,
  versions: ['1.0.0'],
  assessments: ['resolved'],
  expressions: ['MIT'],
  repository_count: repositories.length,
  repository_ids: repositories,
  ...extra,
})

const response = (dependencies: SupplyChainGraphDependency[], extra: Partial<SupplyChainDependencyGraph> = {}): SupplyChainDependencyGraph => ({
  stream: 'github:source',
  repositories_in_scope: 3,
  min_repositories: 2,
  matching: dependencies.length,
  truncated: false,
  dependencies,
  repositories: [1, 2, 3].map((id) => ({ id, name: `acme/r${id}` })),
  ...extra,
})

const shared = response([
  dependency('left-pad', [2, 3], { version_count: 2, versions: ['1.10.0', '1.2.0'], expressions: ['MIT', 'ISC'] }),
  dependency('lodash', [1, 2, 3], { namespace: '@x', assessments: ['resolved', 'conflict'] }),
  dependency('spring', [1, 2], { ecosystem: 'maven', assessments: ['unassessed'] }),
])

describe('buildGraph', () => {
  it('makes one node per dependency, most shared first, and sorts what it lists', () => {
    const graph = buildGraph(shared)
    expect(graph.dependencies.map((item) => item.label)).toEqual(['@x/lodash', 'left-pad', 'spring'])
    const leftPad = graph.dependencies[1]
    expect(leftPad.versions).toEqual(['1.2.0', '1.10.0'])
    expect(leftPad.expressions).toEqual(['ISC', 'MIT'])
    expect(leftPad.repositoryIds).toEqual([2, 3])
    expect(graph.matching).toBe(3)
    expect(graph.partial).toBe(false)
  })

  it('keeps ecosystem and namespace apart', () => {
    const graph = buildGraph(response([dependency('x', [1, 2]), dependency('x', [1, 2], { ecosystem: 'pypi' }), dependency('x', [1, 2], { namespace: '@a' })]))
    expect(new Set(graph.dependencies.map((item) => item.id)).size).toBe(3)
  })

  it('links each dependency to the repositories it lists and names them', () => {
    const graph = buildGraph(response([dependency('a', [8, 9])], { repositories: [{ id: 9, name: 'acme/nine' }, { id: 8 }] }))
    expect(graph.edges).toEqual([
      { repositoryId: 8, dependencyId: graph.dependencies[0].id },
      { repositoryId: 9, dependencyId: graph.dependencies[0].id },
    ])
    expect(graph.repositories.map((repository) => repository.name)).toEqual(['acme/nine', 'Repository 8'])
    expect(graph.repositories[0].dependencyIds).toEqual([graph.dependencies[0].id])
  })

  it('flags conflict and unlicensed packages', () => {
    const graph = buildGraph(shared)
    expect(graph.dependencies.find((item) => item.name === 'lodash')?.risky).toBe(true)
    expect(graph.dependencies.find((item) => item.name === 'spring')?.risky).toBe(false)
  })

  it('keeps the server counts when the lists were cut to the link budget', () => {
    const graph = buildGraph(response([dependency('popular', [1, 2], { repository_count: 1200, version_count: 40 })], { matching: 4812, truncated: true }))
    expect(graph.partial).toBe(true)
    expect(graph.matching).toBe(4812)
    expect(graph.dependencies[0].repositoryCount).toBe(1200)
    expect(graph.edges).toHaveLength(2)
    expect(versionsText(graph.dependencies[0])).toBe('1.0.0 and 39 more')
  })

  it('is empty before the response arrives', () => {
    expect(buildGraph(undefined)).toEqual({ repositories: [], dependencies: [], edges: [], matching: 0, partial: false })
  })
})

describe('layoutGraph', () => {
  it('places repositories left of their dependencies and links them', () => {
    const { nodes, edges } = layoutGraph(buildGraph(shared))
    const repositories = nodes.filter((node) => node.type === 'repository')
    const dependencies = nodes.filter((node) => node.type === 'dependency')
    expect(repositories).toHaveLength(3)
    expect(dependencies).toHaveLength(3)
    expect(Math.max(...repositories.map((node) => node.position.x))).toBeLessThan(Math.min(...dependencies.map((node) => node.position.x)))
    const positions = nodes.map((node) => `${node.position.x},${node.position.y}`)
    expect(new Set(positions).size).toBe(nodes.length)
    expect(edges).toHaveLength(7)
    expect(edges[0].source).toBe(repositoryNodeId(Number(edges[0].source.slice(2))))
    expect(nodes.some((node) => node.id === dependencyNodeId(edges[0].target.slice(2)))).toBe(true)
  })
})
