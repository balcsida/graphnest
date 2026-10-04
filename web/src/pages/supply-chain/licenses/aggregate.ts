import { LICENSE_FAMILIES, licenseFamily, type LicenseFamily } from '@/lib/license-family'
import type { SupplyChainFacet, SupplyChainFacets, SupplyChainOverview } from '@/api/types'
import { LICENSE_ORDER } from '../format'

/** Repositories read one by one for the per-repository chart. */
export const REPOSITORY_FACET_LIMIT = 40

export const TOP_LICENSE_LIMIT = 15

export interface RepositoryFacets {
  repositoryId: number
  name: string
  facets: SupplyChainFacets
}

export interface LicenseCount {
  expression: string
  count: number
}

export interface RepositoryMix {
  repositoryId: number
  name: string
  /** Components with a normalized license expression. */
  total: number
  families: Record<LicenseFamily, number>
  /** The exact expressions behind each family, largest first. */
  expressions: Record<LicenseFamily, LicenseCount[]>
}

const byCountThenValue = (a: SupplyChainFacet, b: SupplyChainFacet) => b.count - a.count || a.value.localeCompare(b.value)

/** Licenses by component count, largest first, at most `limit`. */
export const topLicenses = (facets: SupplyChainFacets | undefined, limit = TOP_LICENSE_LIMIT): SupplyChainFacet[] =>
  [...(facets?.licenses ?? [])].sort(byCountThenValue).slice(0, limit)

const emptyByFamily = <T>(make: () => T) => Object.fromEntries(LICENSE_FAMILIES.map((family) => [family, make()])) as Record<LicenseFamily, T>

function mixOf(entry: RepositoryFacets): RepositoryMix {
  const mix: RepositoryMix = {
    repositoryId: entry.repositoryId,
    name: entry.name,
    total: 0,
    families: emptyByFamily(() => 0),
    expressions: emptyByFamily<LicenseCount[]>(() => []),
  }
  for (const { value, count } of [...entry.facets.licenses].sort(byCountThenValue)) {
    const family = licenseFamily(value)
    mix.families[family] += count
    mix.expressions[family].push({ expression: value, count })
    mix.total += count
  }
  return mix
}

/**
 * Per-repository license mix, largest first (ties by name), at most `topN` rows. Repositories without a license
 * expression have no bar and do not count towards `withLicenses`.
 */
export function repositoryLicenseMix(entries: RepositoryFacets[], topN: number): { rows: RepositoryMix[]; withLicenses: number } {
  const mixes = entries.map(mixOf).filter((mix) => mix.total > 0)
  mixes.sort((a, b) => b.total - a.total || a.name.localeCompare(b.name))
  return { rows: mixes.slice(0, topN), withLicenses: mixes.length }
}

function sumFacet(lists: SupplyChainFacet[][]): SupplyChainFacet[] {
  const counts = new Map<string, number>()
  for (const facet of lists.flat()) counts.set(facet.value, (counts.get(facet.value) ?? 0) + facet.count)
  return [...counts].map(([value, count]) => ({ value, count })).sort(byCountThenValue)
}

/** The facets of a repository scope, as the sum of its repositories' facets. */
export const sumFacets = (stream: string, all: SupplyChainFacets[]): SupplyChainFacets => ({
  stream,
  ecosystems: sumFacet(all.map((facets) => facets.ecosystems)),
  assessments: sumFacet(all.map((facets) => facets.assessments)),
  licenses: sumFacet(all.map((facets) => facets.licenses)),
})

export interface AssessmentTotals {
  assessed: number
  /** Assessed plus unassessed: the denominator of every assessment count. */
  total: number
  /** Whole percent, or undefined when nothing was counted. */
  assessedPercent: number | undefined
}

/** "Assessed" is every coordinate with a recorded status; "unassessed" is reported separately by the overview. */
export function assessmentTotals(overview: SupplyChainOverview): AssessmentTotals {
  const assessed = Object.values(overview.components.assessments ?? {}).reduce((sum, count) => sum + count, 0)
  const total = assessed + overview.components.unassessed
  return { assessed, total, assessedPercent: total > 0 ? Math.round((assessed / total) * 100) : undefined }
}

/** Statuses with a count in the stable display order, "unassessed" last. */
export const statusSlices = (overview: SupplyChainOverview) =>
  LICENSE_ORDER.flatMap((status) => {
    const count = status === 'unassessed' ? overview.components.unassessed : overview.components.assessments?.[status]
    return typeof count === 'number' && count > 0 ? [{ status, count }] : []
  })
