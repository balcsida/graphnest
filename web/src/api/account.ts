import type {
  APITokenList,
  CreateAPITokenRequest,
  CreateDelegationOnlyTokenRequest,
  CreatedAPIToken,
  CreatedDelegationOnlyToken,
  OAuthGrantList,
} from './types'
import { request } from '@/lib/api'

export const getApiTokens = (signal?: AbortSignal) => request<APITokenList>('/v1/account/api-tokens', { signal })
export const createApiToken = (body: CreateAPITokenRequest) =>
  request<CreatedAPIToken>('/v1/account/api-tokens', { method: 'POST', body })
export const revokeApiToken = (id: number) =>
  request<void>(`/v1/account/api-tokens/${encodeURIComponent(id)}`, { method: 'DELETE' })

export const createDelegationToken = (body: CreateDelegationOnlyTokenRequest) =>
  request<CreatedDelegationOnlyToken>('/v1/account/delegation-tokens', { method: 'POST', body })

export const getOAuthGrants = (cursor?: string, signal?: AbortSignal) =>
  request<OAuthGrantList>(cursor ? `/v1/account/oauth-grants?cursor=${encodeURIComponent(cursor)}` : '/v1/account/oauth-grants', { signal })
export const revokeOAuthGrant = (id: number) =>
  request<void>(`/v1/account/oauth-grants/${encodeURIComponent(id)}`, { method: 'DELETE' })
