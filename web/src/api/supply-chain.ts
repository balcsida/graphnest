import type { SupplyChainOverview } from './types'
import { request } from '@/lib/api'

/** A 404 means the supply-chain feature is not enabled on this server. */
export const getSupplyChainOverview = (signal?: AbortSignal) =>
  request<SupplyChainOverview>('/v1/supply-chain/overview', { signal })
