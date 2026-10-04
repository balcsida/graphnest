import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { authConfig, stubFetch } from '@/test/fetch'
import { mountPage, repositoryFixture, session } from '@/test/search-harness'
import SearchPage from './SearchPage'

const sha = 'a'.repeat(40)
const other = 'b'.repeat(40)

const base = {
  'GET /v1/auth/config': { body: authConfig },
  ...session,
  'GET /v1/repositories': { body: { repositories: [repositoryFixture('acme/one', 1)], truncated: false } },
  'POST /v1/search': {
    body: {
      matches: [{ repository: { id: 1, name: 'acme/one', branch: 'main', indexed_sha: sha, web_url: 'https://github.com/acme/one' }, path: 'main.go', sha, line_number: 2, line_start: 2, line_end: 2, preview: 'func Run()', score: 1 }],
      truncated: false,
    },
  },
}

const fileBody = (extra: object = {}) => ({
  body: { repository_id: 1, path: 'main.go', indexed_sha: sha, blob_sha: 'x', content: 'package main\nfunc é😀Run() {}', start_line: 1, end_line: 2, truncated: false, ...extra },
})

async function openFromSearch(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByRole('searchbox', { name: 'Search code' }), 'Run')
  await user.click(screen.getByRole('button', { name: 'Search' }))
  const path = await screen.findByRole('button', { name: 'main.go' })
  await user.click(path)
  return path
}

describe('file viewer', () => {
  it('requests the match line, shows branch · short SHA, focuses the line and restores focus on back', async () => {
    const user = userEvent.setup()
    const calls = stubFetch({ ...base, 'POST /v1/files/read': fileBody() })
    mountPage(<SearchPage />)
    const path = await openFromSearch(user)

    const code = await screen.findByRole('region', { name: 'Indexed file contents' })
    expect(calls.find((call) => call.path === '/v1/files/read')?.body).toEqual({ repository_id: 1, path: 'main.go', start_line: 2 })
    expect(screen.getByText('main · aaaaaaa')).toBeInTheDocument()
    expect(screen.getByText('acme/one / main.go')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open indexed source ↗' })).toHaveAttribute('href', `https://github.com/acme/one/blob/${sha}/main.go#L2`)
    await waitFor(() => expect(code.querySelector('[data-line="2"]')).toHaveFocus())
    expect(code.querySelector('[data-line="2"]')).toHaveAttribute('tabindex', '-1')

    await user.click(screen.getByRole('button', { name: '← Results' }))
    expect(screen.queryByRole('region', { name: 'Indexed file contents' })).not.toBeInTheDocument()
    expect(path).toHaveFocus()
  })

  it('focuses the back button on open and reports a truncated file', async () => {
    const user = userEvent.setup()
    stubFetch({ ...base, 'POST /v1/files/read': fileBody({ truncated: true }) })
    mountPage(<SearchPage />)
    await openFromSearch(user)
    expect(await screen.findByText('File content was truncated.')).toBeInTheDocument()
  })

  it('refuses content from a different indexed revision', async () => {
    const user = userEvent.setup()
    stubFetch({ ...base, 'POST /v1/files/read': fileBody({ indexed_sha: other }) })
    mountPage(<SearchPage />)
    await openFromSearch(user)
    expect(await screen.findByText('Indexed revision changed. Search again.')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Indexed file contents' })).not.toBeInTheDocument()
  })

  it('navigates an identifier with exact offsets and the chosen operation, then opens a location', async () => {
    const user = userEvent.setup()
    const location = {
      repository_id: 1,
      repository_name: 'acme/one',
      branch: 'main',
      web_url: 'https://github.com/acme/one',
      commit: sha,
      path: 'util/helper.go',
      symbol: '',
      start_line: 9,
      start_character: 0,
      end_line: 9,
      end_character: 3,
      position_encoding: 'UTF8CodeUnitOffsetFromLineStart',
      roles: 1,
      approximate: true,
    }
    const calls = stubFetch({
      ...base,
      'POST /v1/files/read': ({ body }) =>
        (body as { path: string }).path === 'main.go' ? fileBody() : { body: { repository_id: 1, path: 'util/helper.go', indexed_sha: sha, blob_sha: 'y', content: 'one\ntwo', start_line: 9, end_line: 10, truncated: false } },
      'POST /v1/scip/navigation': { body: { locations: [location], truncated: false } },
    })
    mountPage(<SearchPage />)
    await openFromSearch(user)
    await user.click(await screen.findByRole('button', { name: 'é' }))

    const panel = await screen.findByRole('complementary', { name: 'Code navigation' })
    expect(await within(panel).findByText('Indexed symbol')).toBeInTheDocument()
    expect(within(panel).getByText('Approximate')).toBeInTheDocument()
    expect(calls.filter((call) => call.path === '/v1/scip/navigation').at(-1)?.body).toEqual({
      repository_id: 1,
      path: 'main.go',
      commit: sha,
      line: 2,
      character_utf8: 5,
      character_utf16: 5,
      character_utf32: 5,
      operation: 'definitions',
    })

    await user.click(within(panel).getByRole('button', { name: 'References' }))
    await waitFor(() => expect(calls.filter((call) => call.path === '/v1/scip/navigation').at(-1)?.body).toMatchObject({ operation: 'references' }))
    expect(within(panel).getByRole('button', { name: 'References' })).toHaveAttribute('aria-pressed', 'true')
    expect(within(panel).getByRole('link', { name: 'Open indexed source ↗' })).toHaveAttribute('href', `https://github.com/acme/one/blob/${sha}/util/helper.go#L9`)

    await user.click(within(panel).getByRole('button', { name: /util\/helper\.go:9/ }))
    expect(await screen.findByText('acme/one / util/helper.go')).toBeInTheDocument()
    expect(calls.filter((call) => call.path === '/v1/files/read').at(-1)?.body).toEqual({ repository_id: 1, path: 'util/helper.go', start_line: 9 })
    expect(screen.queryByRole('complementary', { name: 'Code navigation' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '← Results' }))
    expect(screen.getByRole('button', { name: 'main.go' })).toHaveFocus()
  })
})
