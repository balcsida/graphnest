import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { TokenGate } from '@/components/auth/TokenGate'
import { AuthProvider } from '@/lib/auth'
import { stubFetch, unauthenticated } from '@/test/fetch'

const mount = () =>
  render(
    <QueryClientProvider client={new QueryClient()}>
      <AuthProvider>
        <TokenGate />
      </AuthProvider>
    </QueryClientProvider>,
  )

const config = (overrides: object) => ({
  body: { token_login: false, break_glass: false, file_reads: false, providers: [], ...overrides },
})

describe('TokenGate', () => {
  it('renders only what the config enables', async () => {
    stubFetch({
      'GET /v1/auth/config': config({ providers: [{ id: 'github', label: '<b>GitHub</b>', login_url: '/auth/github/login' }] }),
      'GET /v1/auth/session': unauthenticated,
    })
    mount()
    const link = await screen.findByRole('link', { name: '<b>GitHub</b>' })
    expect(link).toHaveAttribute('href', '/auth/github/login')
    expect(screen.queryByLabelText('Bearer token')).not.toBeInTheDocument()
    expect(screen.queryByRole('form', { name: /administrator recovery/i })).not.toBeInTheDocument()
  })

  it('shows the bearer form when token_login is true', async () => {
    stubFetch({ 'GET /v1/auth/config': config({ token_login: true }), 'GET /v1/auth/session': unauthenticated })
    mount()
    expect(await screen.findByLabelText('Bearer token')).toBeInTheDocument()
    expect(screen.queryByLabelText('User name')).not.toBeInTheDocument()
  })

  it('shows the recovery form only when break_glass is true and signs in', async () => {
    const user = userEvent.setup()
    let signedIn = false
    const calls = stubFetch({
      'GET /v1/auth/config': config({ break_glass: true }),
      'GET /v1/auth/session': () => (signedIn ? { body: { method: 'local' } } : unauthenticated),
      'POST /auth/local': () => {
        signedIn = true
        return { status: 204 }
      },
    })
    mount()
    await user.type(await screen.findByLabelText('User name'), 'admin')
    await user.type(screen.getByLabelText('Current password'), 'secret')
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(calls.some((call) => call.path === '/auth/local')).toBe(true))
    expect(calls.find((call) => call.path === '/auth/local')?.body).toEqual({ user_name: 'admin', password: 'secret' })
    expect(calls.filter((call) => call.path === '/v1/auth/session')).toHaveLength(2)
  })

  it('rotates when a new password is given, clears the fields and reports throttling', async () => {
    const user = userEvent.setup()
    const calls = stubFetch({
      'GET /v1/auth/config': config({ break_glass: true }),
      'GET /v1/auth/session': unauthenticated,
      'POST /auth/local/rotate': { status: 429 },
    })
    mount()
    await user.type(await screen.findByLabelText('User name'), 'admin')
    await user.type(screen.getByLabelText('Current password'), 'old')
    await user.type(screen.getByLabelText(/new password/i), 'a-new-password-16')
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByText('Try again later.')).toBeInTheDocument()
    expect(calls.find((call) => call.path === '/auth/local/rotate')?.body).toEqual({ user_name: 'admin', current_password: 'old', new_password: 'a-new-password-16' })
    expect(screen.getByLabelText('Current password')).toHaveValue('')
    expect(screen.getByLabelText(/new password/i)).toHaveValue('')
  })

  it('reports other failures generically', async () => {
    const user = userEvent.setup()
    stubFetch({
      'GET /v1/auth/config': config({ break_glass: true }),
      'GET /v1/auth/session': unauthenticated,
      'POST /auth/local': unauthenticated,
    })
    mount()
    await user.type(await screen.findByLabelText('User name'), 'admin')
    await user.type(screen.getByLabelText('Current password'), 'x')
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByText('Sign-in failed.')).toBeInTheDocument()
  })
})
