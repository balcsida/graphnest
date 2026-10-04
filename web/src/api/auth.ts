import type { AuthConfig, AuthSession } from './types'
import { request } from '@/lib/api'

export const getAuthConfig = (signal?: AbortSignal) => request<AuthConfig>('/v1/auth/config', { signal })

// A 401 here means "no credential yet", not a lost one, so it never trips the unauthorized handler.
export const getAuthSession = (signal?: AbortSignal) =>
  request<AuthSession>('/v1/auth/session', { signal, keepCredentialOn401: true })

export const logout = () => request<void>('/auth/logout', { method: 'POST', keepCredentialOn401: true })

export const localSignIn = (user_name: string, password: string) =>
  request<void>('/auth/local', { method: 'POST', body: { user_name, password }, keepCredentialOn401: true })

export const localRotate = (user_name: string, current_password: string, new_password: string) =>
  request<void>('/auth/local/rotate', {
    method: 'POST',
    body: { user_name, current_password, new_password },
    keepCredentialOn401: true,
  })
