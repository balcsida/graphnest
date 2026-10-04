import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { useAuth } from '@/lib/auth'
import { authConfig, stubFetch } from '@/test/fetch'
import { mountPage, repositoryFixture, session } from '@/test/search-harness'
import SearchPage from './SearchPage'

function SignOut() {
  const { signOut } = useAuth()
  return <button onClick={() => void signOut()}>Sign out now</button>
}

const config = {
  'GET /v1/auth/config': { body: { ...authConfig, token_login: true } },
  ...session,
  'GET /v1/repositories': { body: { repositories: [repositoryFixture('acme/one', 1)], truncated: false } },
}

describe('search state', () => {
  it('clears the query, results and scope on sign-out', async () => {
    const user = userEvent.setup()
    stubFetch({
      ...config,
      'POST /v1/search': { body: { matches: [], truncated: false } },
      'POST /auth/logout': { status: 204 },
    })
    mountPage(
      <>
        <SearchPage />
        <SignOut />
      </>,
      '/',
      false,
    )
    const input = await screen.findByRole('searchbox', { name: 'Search code' })
    await user.type(input, 'abc')
    await user.click(screen.getByRole('button', { name: 'All repositories' }))
    await user.click(screen.getByLabelText('All authorized repositories'))
    await user.click(screen.getByLabelText('acme/one'))
    await user.click(screen.getByRole('button', { name: 'Search' }))
    expect(await screen.findByText('No matches. Try file:.go NewService')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Sign out now' }))
    await waitFor(() => expect(screen.getByRole('searchbox', { name: 'Search code' })).toHaveValue(''))
    expect(screen.getByRole('heading', { name: 'No search yet' })).toBeInTheDocument()
    expect(screen.queryByText('No matches. Try file:.go NewService')).not.toBeInTheDocument()
  })

  it('aborts the previous search when a new one is submitted', async () => {
    const user = userEvent.setup()
    stubFetch({ ...config, 'POST /v1/search': { body: { matches: [], truncated: false } } })
    const real = globalThis.fetch
    const signals: AbortSignal[] = []
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input) !== '/v1/search') return real(input, init)
      if (init?.signal) signals.push(init.signal)
      return signals.length === 1 ? new Promise<Response>(() => {}) : real(input, init)
    })
    mountPage(<SearchPage />)
    const input = await screen.findByRole('searchbox', { name: 'Search code' })
    await user.type(input, 'one')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() => expect(signals).toHaveLength(1))
    await user.type(input, ' two')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() => expect(signals).toHaveLength(2))
    expect(signals[0].aborted).toBe(true)
    expect(signals[1].aborted).toBe(false)
    expect(await screen.findByText('No matches. Try file:.go NewService')).toBeInTheDocument()
  })
})
