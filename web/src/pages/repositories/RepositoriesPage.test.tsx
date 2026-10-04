import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { authConfig, stubFetch } from '@/test/fetch'
import { mountPage, repositoryFixture, session } from '@/test/search-harness'
import RepositoriesPage from './RepositoriesPage'

describe('RepositoriesPage', () => {
  it('renders the table columns, formatted cells and https-only links', async () => {
    stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      ...session,
      'GET /v1/repositories': {
        body: {
          repositories: [
            repositoryFixture('octocat/Hello-World', 1, { branch: 'master', indexed_sha: '7fd1a60b01f91b314f59955a4e4d4e80d8edf11d', last_indexed_at: '2026-01-02T03:04:00Z' }),
            repositoryFixture('acme/odd', 2, { branch: '', indexed_sha: '', status: 'weird', web_url: 'http://insecure.example/acme/odd' }),
          ],
          truncated: false,
        },
      },
    })
    mountPage(<RepositoriesPage />)
    expect(await screen.findByRole('heading', { name: 'Repositories' })).toBeInTheDocument()
    expect(screen.getByText('Everything your token is authorized to search.')).toBeInTheDocument()
    const hello = await screen.findByRole('row', { name: /octocat\/Hello-World/ })
    expect(screen.getAllByRole('columnheader').map((header) => header.textContent)).toEqual(['Repository', 'Branch', 'Status', 'Indexed SHA', 'Last indexed', 'Actions'])

    expect(within(hello).getByText('master')).toBeInTheDocument()
    expect(within(hello).getByText('7fd1a60')).toBeInTheDocument()
    expect(within(hello).getByText('ready')).toBeInTheDocument()
    expect(within(hello).queryByText('Never')).not.toBeInTheDocument()
    const link = within(hello).getByRole('link', { name: 'Open ↗' })
    expect(link).toHaveAttribute('href', 'https://github.com/octocat/Hello-World')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    expect(link).toHaveAttribute('target', '_blank')

    const odd = screen.getByRole('row', { name: /acme\/odd/ })
    expect(within(odd).getByText('weird')).toBeInTheDocument()
    expect(within(odd).getByText('Never')).toBeInTheDocument()
    expect(within(odd).getAllByText('—')).toHaveLength(3)
    expect(within(odd).queryByRole('link')).not.toBeInTheDocument()
  })

  it('loads more with the cursor and de-duplicates by name', async () => {
    const user = userEvent.setup()
    stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      ...session,
      'GET /v1/repositories': { body: { repositories: [repositoryFixture('acme/one', 1)], truncated: true, next_cursor: 'next' } },
      'GET /v1/repositories?cursor=next': { body: { repositories: [repositoryFixture('acme/one', 1), repositoryFixture('acme/two', 2)], truncated: false } },
    })
    mountPage(<RepositoriesPage />)
    expect(await screen.findByText('Showing the first 1 authorized repositories.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Load more repositories' }))
    expect(await screen.findByRole('row', { name: /acme\/two/ })).toBeInTheDocument()
    expect(screen.getAllByRole('row', { name: /acme\/one/ })).toHaveLength(1)
    expect(screen.queryByRole('button', { name: 'Load more repositories' })).not.toBeInTheDocument()
  })

  it('explains an empty list and a failed load', async () => {
    stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, 'GET /v1/repositories': { body: { repositories: [], truncated: false } } })
    mountPage(<RepositoriesPage />)
    expect(await screen.findByText('No authorized repositories are available for this token.')).toBeInTheDocument()
  })

  it('shows an alert when the list is unavailable', async () => {
    stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, 'GET /v1/repositories': { status: 503, body: { error: { code: 'x', message: 'down', request_id: 'r', retryable: true } } } })
    mountPage(<RepositoriesPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Repository list is unavailable. Search still covers every authorized repository.')
  })
})
