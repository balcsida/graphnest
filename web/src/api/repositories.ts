import type { RepositoryList } from './types'
import { request } from '@/lib/api'

export const listRepositories = (cursor?: string, signal?: AbortSignal) =>
  request<RepositoryList>(`/v1/repositories${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`, { signal })
