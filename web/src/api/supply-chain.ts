import type {
  SupplyChainCollectionList,
  SupplyChainComponentDetail,
  SupplyChainComponentList,
  SupplyChainFacets,
  SupplyChainJob,
  SupplyChainOverview,
  SupplyChainPortfolioComponentDetail,
  SupplyChainPortfolioComponentList,
  SupplyChainRefreshResponse,
  SupplyChainRepositoryStatus,
  SupplyChainSnapshotComparison,
  SupplyChainSnapshotList,
} from './types'
import { request, requestBlob } from '@/lib/api'

/** The stream every legacy request used and the server's default. */
export const DEFAULT_STREAM = 'github:source'

type Value = string | number | undefined

/** Query string in the given order; undefined and empty values are left out. */
function queryString(entries: [string, Value | readonly number[]][]): string {
  const query = new URLSearchParams()
  for (const [name, value] of entries) {
    if (Array.isArray(value)) value.forEach((item) => query.append(name, String(item)))
    else if (value !== undefined && value !== '') query.append(name, String(value))
  }
  const text = query.toString()
  return text ? `?${text}` : ''
}

export interface OverviewParams {
  stream?: string
  /** Narrows the scope to these GitHub repository IDs (at most 200). */
  repository_id?: number[]
}

/** A 404 means the supply-chain feature is not enabled on this server. */
export const getSupplyChainOverview = (params: OverviewParams = {}, signal?: AbortSignal) =>
  request<SupplyChainOverview>(
    `/v1/supply-chain/overview${queryString([
      ['stream', params.stream],
      ['repository_id', params.repository_id],
    ])}`,
    { signal },
  )

export interface FacetsParams {
  stream?: string
  /** GitHub repository ID. */
  repository_id?: number
}

export const getSupplyChainFacets = (params: FacetsParams = {}, signal?: AbortSignal) =>
  request<SupplyChainFacets>(
    `/v1/supply-chain/facets${queryString([
      ['stream', params.stream],
      ['repository_id', params.repository_id],
    ])}`,
    { signal },
  )

export interface PortfolioComponentsParams {
  stream?: string
  /** GitHub repository ID. */
  repository_id?: number
  ecosystem?: string
  assessment?: string
  /** Exact normalized SPDX expression. */
  license?: string
  /** Case-insensitive substring of name or purl. */
  q?: string
  /** next_cursor of the previous page; it is bound to the filter set that produced it. */
  cursor?: string
  limit?: number
}

export const listSupplyChainComponents = (params: PortfolioComponentsParams = {}, signal?: AbortSignal) =>
  request<SupplyChainPortfolioComponentList>(
    `/v1/supply-chain/components${queryString([
      ['stream', params.stream],
      ['repository_id', params.repository_id],
      ['limit', params.limit],
      ['ecosystem', params.ecosystem],
      ['assessment', params.assessment],
      ['license', params.license],
      ['q', params.q],
      ['cursor', params.cursor],
    ])}`,
    { signal },
  )

export const getSupplyChainComponent = (key: string, params: { stream?: string; repository_id?: number } = {}, signal?: AbortSignal) =>
  request<SupplyChainPortfolioComponentDetail>(
    `/v1/supply-chain/components/${encodeURIComponent(key)}${queryString([
      ['stream', params.stream],
      ['repository_id', params.repository_id],
    ])}`,
    { signal },
  )

const repositoryPath = (id: number) => `/v1/supply-chain/repositories/${encodeURIComponent(String(id))}`

export const getSupplyChainRepository = (id: number, params: { stream?: string } = {}, signal?: AbortSignal) =>
  request<SupplyChainRepositoryStatus>(`${repositoryPath(id)}${queryString([['stream', params.stream]])}`, { signal })

export interface RepositoryComponentsParams {
  stream?: string
  snapshot_id?: number
  q?: string
  cursor?: string
  limit?: number
}

export const listSupplyChainRepositoryComponents = (id: number, params: RepositoryComponentsParams = {}, signal?: AbortSignal) =>
  request<SupplyChainComponentList>(
    `${repositoryPath(id)}/components${queryString([
      ['stream', params.stream],
      ['snapshot_id', params.snapshot_id],
      ['limit', params.limit],
      ['q', params.q],
      ['cursor', params.cursor],
    ])}`,
    { signal },
  )

export const getSupplyChainRepositoryComponent = (
  id: number,
  params: { element: string; stream?: string; snapshot_id?: number },
  signal?: AbortSignal,
) =>
  request<SupplyChainComponentDetail>(
    `${repositoryPath(id)}/component${queryString([
      ['element', params.element],
      ['stream', params.stream],
      ['snapshot_id', params.snapshot_id],
    ])}`,
    { signal },
  )

export const listSupplyChainSnapshots = (id: number, params: { stream?: string; limit?: number } = {}, signal?: AbortSignal) =>
  request<SupplyChainSnapshotList>(
    `${repositoryPath(id)}/snapshots${queryString([
      ['stream', params.stream],
      ['limit', params.limit],
    ])}`,
    { signal },
  )

export const listSupplyChainCollections = (
  id: number,
  params: { stream?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
) =>
  request<SupplyChainCollectionList>(
    `${repositoryPath(id)}/collections${queryString([
      ['stream', params.stream],
      ['limit', params.limit],
      ['cursor', params.cursor],
    ])}`,
    { signal },
  )

/** Administrator only; answers 202 with the queued job. */
export const refreshSupplyChainRepository = (id: number, params: { stream?: string } = {}) =>
  request<SupplyChainRefreshResponse>(`${repositoryPath(id)}/refresh${queryString([['stream', params.stream]])}`, { method: 'POST' })

export const getSupplyChainJob = (id: number, signal?: AbortSignal) =>
  request<SupplyChainJob>(`/v1/supply-chain/jobs/${encodeURIComponent(String(id))}`, { signal })

export const compareSupplyChainSnapshots = (params: { repository_id: number; base: number; head: number }, signal?: AbortSignal) =>
  request<SupplyChainSnapshotComparison>(
    `/v1/supply-chain/compare${queryString([
      ['repository_id', params.repository_id],
      ['base', params.base],
      ['head', params.head],
    ])}`,
    { signal },
  )

/** CSV of one authorized snapshot's components; fetched with credentials because a bare link would not carry a bearer token. */
export const componentsCsvPath = (id: number, params: { stream?: string; snapshot_id?: number }) =>
  `/v1/supply-chain/exports/${encodeURIComponent(String(id))}/components.csv${queryString([
    ['stream', params.stream],
    ['snapshot_id', params.snapshot_id],
  ])}`

/** Derived SPDX 2.3 JSON of one authorized snapshot (GraphNest assessments as licenseComments only). */
export const derivedSpdxPath = (id: number, params: { stream?: string; snapshot_id?: number }) =>
  `/v1/supply-chain/exports/${encodeURIComponent(String(id))}/derived.spdx.json${queryString([
    ['stream', params.stream],
    ['snapshot_id', params.snapshot_id],
  ])}`

/** Fetches a document or export with the caller's credentials and saves it under the server's filename or the fallback. */
export async function downloadSupplyChainFile(path: string, fallbackName: string): Promise<void> {
  // The bearer token must only go to this API, never to a path the server named off-origin.
  if (!path.startsWith('/v1/') || path.startsWith('//')) throw new Error('Refusing to download from an unexpected path.')
  const { blob, filename } = await requestBlob(path)
  const href = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = href
  link.download = filename ?? fallbackName
  link.click()
  setTimeout(() => URL.revokeObjectURL(href), 1000)
}
