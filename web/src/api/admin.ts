import type { AdminOverview } from './types'
import { request } from '@/lib/api'

/** Succeeds only for administrators; 401, 403 and 404 all mean "not an administrator". */
export const getAdminOverview = (signal?: AbortSignal) => request<AdminOverview>('/v1/admin/overview', { signal })
