import type { ReadFileRequest, ReadFileResponse, ScipNavigationRequest, ScipNavigationResponse, SearchRequest, SearchResponse } from './types'
import { request } from '@/lib/api'

export const searchCode = (body: SearchRequest, signal?: AbortSignal) =>
  request<SearchResponse>('/v1/search', { method: 'POST', body, signal })

export const readFile = (body: ReadFileRequest, signal?: AbortSignal) =>
  request<ReadFileResponse>('/v1/files/read', { method: 'POST', body, signal })

export const scipNavigation = (body: ScipNavigationRequest, signal?: AbortSignal) =>
  request<ScipNavigationResponse>('/v1/scip/navigation', { method: 'POST', body, signal })
