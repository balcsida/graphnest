import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect } from 'react'
import { act, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ApiError, request } from '@/lib/api'
import { AuthProvider, useAuth, type Auth } from '@/lib/auth'
import { authConfig, stubFetch, unauthenticated } from '@/test/fetch'

const latest: { auth?: Auth } = {}
// Tests drive the provider through the latest hook value.
const current = () => latest.auth!

function Probe() {
  const auth = useAuth()
  useEffect(() => {
    latest.auth = auth
  })
  return <p>{auth.status}:{auth.method ?? 'none'}</p>
}

function mount() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <AuthProvider>
        <Probe />
      </AuthProvider>
    </QueryClientProvider>,
  )
}

const bearerOnly = (calls: ReturnType<typeof stubFetch>) =>
  calls.filter((call) => call.path === '/v1/auth/session').map((call) => call.headers.get('Authorization'))

describe('auth state machine', () => {
  it('prefers a browser session and ignores the stored token', async () => {
    sessionStorage.setItem('graphnest_token', 'stored')
    const calls = stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      'GET /v1/auth/session': { body: { method: 'oidc' } },
    })
    mount()
    expect(await screen.findByText('authenticated:oidc')).toBeInTheDocument()
    expect(bearerOnly(calls)).toEqual([null])
  })

  it('falls back to the stored bearer token after a 401 session probe', async () => {
    sessionStorage.setItem('graphnest_token', 'stored')
    const calls = stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      'GET /v1/auth/session': ({ headers }) => (headers.get('Authorization') ? { body: { method: 'bearer' } } : unauthenticated),
    })
    mount()
    expect(await screen.findByText('authenticated:bearer')).toBeInTheDocument()
    expect(bearerOnly(calls)).toEqual([null, 'Bearer stored'])
  })

  it('shows the gate when there is no session and no token', async () => {
    stubFetch({ 'GET /v1/auth/config': { body: authConfig }, 'GET /v1/auth/session': unauthenticated })
    mount()
    expect(await screen.findByText('gate:none')).toBeInTheDocument()
  })

  it('migrates the legacy admin token key once', async () => {
    sessionStorage.setItem('graphnest_admin_token', 'legacy')
    stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      'GET /v1/auth/session': ({ headers }) => (headers.get('Authorization') === 'Bearer legacy' ? { body: { method: 'bearer' } } : unauthenticated),
    })
    mount()
    expect(await screen.findByText('authenticated:bearer')).toBeInTheDocument()
    expect(sessionStorage.getItem('graphnest_token')).toBe('legacy')
    expect(sessionStorage.getItem('graphnest_admin_token')).toBeNull()
  })

  it('clears a stored token the server rejects', async () => {
    sessionStorage.setItem('graphnest_token', 'stale')
    stubFetch({ 'GET /v1/auth/config': { body: authConfig }, 'GET /v1/auth/session': unauthenticated })
    mount()
    expect(await screen.findByText('gate:none')).toBeInTheDocument()
    expect(sessionStorage.getItem('graphnest_token')).toBeNull()
  })

  it('keeps the session when POST /auth/logout fails', async () => {
    stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      'GET /v1/auth/session': { body: { method: 'oauth' } },
      'POST /auth/logout': { status: 500 },
    })
    mount()
    await screen.findByText('authenticated:oauth')
    await act(async () => {
      await expect(current().signOut()).rejects.toBeInstanceOf(ApiError)
    })
    expect(screen.getByText('authenticated:oauth')).toBeInTheDocument()
  })

  it('drops to the gate after a successful logout', async () => {
    stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      'GET /v1/auth/session': { body: { method: 'oauth' } },
      'POST /auth/logout': { status: 204 },
    })
    mount()
    await screen.findByText('authenticated:oauth')
    await act(() => current().signOut())
    expect(screen.getByText('gate:none')).toBeInTheDocument()
  })

  it('signs out of bearer mode without calling the server and runs principal resets', async () => {
    const calls = stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      'GET /v1/auth/session': ({ headers }) => (headers.get('Authorization') ? { body: { method: 'bearer' } } : unauthenticated),
    })
    mount()
    await screen.findByText('gate:none')
    let resets = 0
    current().registerPrincipalReset(() => resets++)
    await act(() => current().signInWithToken('typed'))
    expect(sessionStorage.getItem('graphnest_token')).toBe('typed')
    await act(() => current().signOut())
    expect(sessionStorage.getItem('graphnest_token')).toBeNull()
    expect(resets).toBe(1)
    expect(calls.some((call) => call.path === '/auth/logout')).toBe(false)
  })

  it('rejects an invalid token without storing it', async () => {
    stubFetch({ 'GET /v1/auth/config': { body: authConfig }, 'GET /v1/auth/session': unauthenticated })
    mount()
    await screen.findByText('gate:none')
    await act(async () => {
      await expect(current().signInWithToken('bad')).rejects.toBeInstanceOf(ApiError)
    })
    expect(sessionStorage.getItem('graphnest_token')).toBeNull()
  })

  it('clears the token on a 401 from any API call but keeps it on 403', async () => {
    stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      'GET /v1/auth/session': ({ headers }) => (headers.get('Authorization') ? { body: { method: 'bearer' } } : unauthenticated),
      'GET /v1/forbidden': { status: 403 },
      'GET /v1/expired': unauthenticated,
    })
    sessionStorage.setItem('graphnest_token', 'live')
    mount()
    await screen.findByText('authenticated:bearer')
    await act(async () => {
      await expect(request('/v1/forbidden')).rejects.toMatchObject({ status: 403 })
    })
    expect(sessionStorage.getItem('graphnest_token')).toBe('live')
    expect(screen.getByText('authenticated:bearer')).toBeInTheDocument()
    await act(async () => {
      await expect(request('/v1/expired')).rejects.toMatchObject({ status: 401, body: { error: { code: 'unauthenticated' } } })
    })
    await waitFor(() => expect(screen.getByText('gate:none')).toBeInTheDocument())
    expect(sessionStorage.getItem('graphnest_token')).toBeNull()
  })
})
