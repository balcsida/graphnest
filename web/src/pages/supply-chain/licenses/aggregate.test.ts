import { describe, expect, it } from 'vitest'
import type { SupplyChainFacets, SupplyChainOverview } from '@/api/types'
import { assessmentTotals, repositoryLicenseMix, statusSlices, sumFacets, topLicenses } from './aggregate'

const facets = (licenses: [string, number][]): SupplyChainFacets => ({
  stream: 'github:source',
  ecosystems: [{ value: 'npm', count: 1 }],
  assessments: [{ value: 'resolved', count: 1 }],
  licenses: licenses.map(([value, count]) => ({ value, count })),
})

describe('topLicenses', () => {
  it('orders by count then name and cuts to the limit', () => {
    const all = facets([['B', 2], ['A', 2], ['C', 9], ['D', 1]])
    expect(topLicenses(all, 3).map((facet) => facet.value)).toEqual(['C', 'A', 'B'])
    expect(all.licenses[0].value).toBe('B')
    expect(topLicenses(undefined)).toEqual([])
  })
})

describe('repositoryLicenseMix', () => {
  const entries = [
    { repositoryId: 1, name: 'acme/small', facets: facets([['MIT', 2]]) },
    { repositoryId: 2, name: 'acme/big', facets: facets([['MIT', 5], ['Apache-2.0 AND MIT', 1], ['GPL-3.0-only', 3], ['NOASSERTION', 1], ['Odd-1.0', 2]]) },
    { repositoryId: 3, name: 'acme/empty', facets: facets([]) },
    { repositoryId: 4, name: 'acme/aaa', facets: facets([['MPL-2.0', 2]]) },
  ]

  it('stacks by family with the exact expressions and drops repositories without licenses', () => {
    const { rows, withLicenses } = repositoryLicenseMix(entries, 10)
    expect(withLicenses).toBe(3)
    expect(rows.map((row) => row.name)).toEqual(['acme/big', 'acme/aaa', 'acme/small'])
    const [big] = rows
    expect(big.total).toBe(12)
    expect(big.families).toEqual({ permissive: 6, weak_copyleft: 0, strong_copyleft: 3, other: 2, unknown: 1 })
    expect(big.expressions.permissive).toEqual([
      { expression: 'MIT', count: 5 },
      { expression: 'Apache-2.0 AND MIT', count: 1 },
    ])
  })

  it('keeps the top N and still reports how many repositories had licenses', () => {
    const { rows, withLicenses } = repositoryLicenseMix(entries, 1)
    expect(rows.map((row) => row.name)).toEqual(['acme/big'])
    expect(withLicenses).toBe(3)
  })
})

describe('sumFacets', () => {
  it('adds counts per value', () => {
    const sum = sumFacets('github:source', [facets([['MIT', 2]]), facets([['MIT', 3], ['ISC', 1]])])
    expect(sum.licenses).toEqual([{ value: 'MIT', count: 5 }, { value: 'ISC', count: 1 }])
    expect(sum.ecosystems).toEqual([{ value: 'npm', count: 2 }])
  })
})

describe('totals', () => {
  const overview = { components: { assessments: { resolved: 3, conflict: 1 }, unassessed: 4 } } as unknown as SupplyChainOverview
  it('names assessed and the denominator', () => {
    expect(assessmentTotals(overview)).toEqual({ assessed: 4, total: 8, assessedPercent: 50 })
    const none = { components: { assessments: {}, unassessed: 0 } } as unknown as SupplyChainOverview
    expect(assessmentTotals(none).assessedPercent).toBeUndefined()
  })
  it('lists statuses in display order', () => {
    expect(statusSlices(overview)).toEqual([
      { status: 'resolved', count: 3 },
      { status: 'conflict', count: 1 },
      { status: 'unassessed', count: 4 },
    ])
  })
})
