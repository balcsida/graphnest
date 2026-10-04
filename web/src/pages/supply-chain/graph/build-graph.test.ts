import { describe, expect, it } from 'vitest'
import type { SupplyChainPortfolioComponent, SupplyChainPortfolioComponentList } from '@/api/types'
import { SCAN_PAGE_CAP, buildGraph, dependencyNodeId, layoutGraph, repositoryNodeId, scanComponents, truncationNotice, type GraphFilters } from './build-graph'

const component = (name: string, version: string, repositories: number[], extra: Partial<SupplyChainPortfolioComponent> = {}): SupplyChainPortfolioComponent => ({
  key: `npm||${name}|${version}`,
  ecosystem: 'npm',
  name,
  version,
  repository_count: repositories.length,
  occurrence_count: repositories.length,
  assessment: 'resolved',
  assessment_statuses: ['resolved'],
  expression: 'MIT',
  declared_raw: [],
  newest_collected_at: '2026-02-01T10:00:00Z',
  oldest_collected_at: '2026-01-20T08:00:00Z',
  repositories: repositories.map((id) => ({ id, name: `acme/r${id}` })),
  ...extra,
})

const filters: GraphFilters = { minRepositories: 2, topN: 150 }

const scanned = [
  component('left-pad', '1.0.0', [1, 2]),
  component('left-pad', '1.1.0', [2, 3], { expression: 'ISC' }),
  component('solo', '1.0.0', [1]),
  component('lodash', '4.0.0', [1, 2, 3], { namespace: '@x', assessment: 'conflict', assessment_statuses: ['conflict'] }),
  component('spring', '5.0.0', [1, 2], { ecosystem: 'maven', assessment: 'unassessed', assessment_statuses: ['unassessed'] }),
]

describe('buildGraph', () => {
  it('groups coordinates across versions into one package node', () => {
    const graph = buildGraph(scanned, { ...filters, minRepositories: 1 })
    const leftPad = graph.dependencies.find((dependency) => dependency.name === 'left-pad')
    expect(graph.dependencies.filter((dependency) => dependency.name === 'left-pad')).toHaveLength(1)
    expect(leftPad?.versions).toEqual(['1.0.0', '1.1.0'])
    expect(leftPad?.repositoryIds).toEqual([1, 2, 3])
    expect(leftPad?.expressions).toEqual(['ISC', 'MIT'])
    expect(graph.packages).toBe(4)
  })

  it('keeps ecosystem and namespace apart', () => {
    const graph = buildGraph([component('x', '1', [1, 2]), component('x', '1', [1, 2], { ecosystem: 'pypi' }), component('x', '1', [1, 2], { namespace: '@a' })], filters)
    expect(graph.dependencies).toHaveLength(3)
  })

  it('applies the minimum shared repositories and orders by repository count', () => {
    const graph = buildGraph(scanned, filters)
    expect(graph.dependencies.map((dependency) => dependency.label)).toEqual(['@x/lodash', 'left-pad', 'spring'])
    expect(buildGraph(scanned, { ...filters, minRepositories: 3 }).dependencies.map((dependency) => dependency.label)).toEqual(['@x/lodash', 'left-pad'])
    expect(buildGraph(scanned, { ...filters, minRepositories: 1 }).dependencies.map((dependency) => dependency.name)).toContain('solo')
  })

  it('filters by ecosystem, assessment and text', () => {
    expect(buildGraph(scanned, { ...filters, ecosystem: 'maven' }).dependencies.map((dependency) => dependency.name)).toEqual(['spring'])
    expect(buildGraph(scanned, { ...filters, assessment: 'conflict' }).dependencies.map((dependency) => dependency.name)).toEqual(['lodash'])
    expect(buildGraph(scanned, { ...filters, q: 'LEFT' }).dependencies.map((dependency) => dependency.name)).toEqual(['left-pad'])
  })

  it('cuts to the top N and reports how many matched', () => {
    const graph = buildGraph(scanned, { ...filters, topN: 1 })
    expect(graph.dependencies.map((dependency) => dependency.name)).toEqual(['lodash'])
    expect(graph.matching).toBe(3)
    expect(graph.edges).toHaveLength(3)
    expect(graph.repositories.map((repository) => repository.id)).toEqual([1, 2, 3])
  })

  it('flags conflict and unlicensed packages and names repositories', () => {
    const graph = buildGraph(scanned, filters, new Map([[9, 'acme/nine']]))
    expect(graph.dependencies.find((dependency) => dependency.name === 'lodash')?.risky).toBe(true)
    expect(graph.dependencies.find((dependency) => dependency.name === 'spring')?.risky).toBe(false)
    const unnamed = buildGraph([component('a', '1', [8, 9], { repositories: [{ id: 8 }, { id: 9 }] })], filters, new Map([[9, 'acme/nine']]))
    expect(unnamed.repositories.map((repository) => repository.name)).toEqual(['acme/nine', 'Repository 8'])
  })
})

describe('scanComponents', () => {
  const page = (index: number, more: boolean): SupplyChainPortfolioComponentList => ({
    stream: 'github:source',
    repositories_in_scope: 1,
    components: [component(`p${index}`, '1', [1])],
    truncated: more,
    next_cursor: more ? `c${index + 1}` : undefined,
  })

  it('follows cursors to the end', async () => {
    const cursors: (string | undefined)[] = []
    const scan = await scanComponents(async (cursor) => {
      cursors.push(cursor)
      return page(cursors.length, cursors.length < 3)
    })
    expect(cursors).toEqual([undefined, 'c2', 'c3'])
    expect(scan).toMatchObject({ pages: 3, capped: false })
    expect(scan.components).toHaveLength(3)
    expect(truncationNotice(scan, 3)).toBeUndefined()
  })

  it('stops at the page cap and feeds the truncation notice', async () => {
    let calls = 0
    const scan = await scanComponents(async () => page(++calls, true))
    expect(calls).toBe(SCAN_PAGE_CAP)
    expect(scan.capped).toBe(true)
    expect(truncationNotice(scan, 5000)).toBe(`Based on the first ${SCAN_PAGE_CAP} of 5000 unique coordinates; narrow the filters to see the rest.`)
  })
})

describe('layoutGraph', () => {
  it('places repositories left of their dependencies and links them', () => {
    const { nodes, edges } = layoutGraph(buildGraph(scanned, filters))
    const repositories = nodes.filter((node) => node.type === 'repository')
    const dependencies = nodes.filter((node) => node.type === 'dependency')
    expect(repositories).toHaveLength(3)
    expect(dependencies).toHaveLength(3)
    expect(Math.max(...repositories.map((node) => node.position.x))).toBeLessThan(Math.min(...dependencies.map((node) => node.position.x)))
    expect(edges).toHaveLength(8)
    expect(edges[0].source).toBe(repositoryNodeId(Number(edges[0].source.slice(2))))
    expect(nodes.some((node) => node.id === dependencyNodeId(edges[0].target.slice(2)))).toBe(true)
  })
})
