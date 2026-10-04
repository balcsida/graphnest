import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { authConfig, stubFetch } from '@/test/fetch'
import { mountPage, repositoryFixture, session } from '@/test/search-harness'
import SearchPage from './SearchPage'

const sha = '7fd1a60b01f91b314f59955a4e4d4e80d8edf11d'
const matchOf = (name: string, path: string, line: number, preview: string) => ({
  repository: { id: name === 'acme/one' ? 1 : 2, name, branch: 'main', indexed_sha: sha, web_url: `https://github.com/${name}` },
  path,
  sha,
  line_number: line,
  line_start: line,
  line_end: line,
  preview,
  score: 1,
})

const repositories = {
  'GET /v1/repositories': { body: { repositories: [repositoryFixture('acme/one', 1), repositoryFixture('acme/two', 2)], truncated: false } },
}

const searchBody = {
  matches: [matchOf('acme/one', 'src/main.go', 10, 'func New() {\n'), matchOf('acme/one', 'src/main.go', 20, 'return New()'), matchOf('acme/two', 'a b/README.md', 1, 'New')],
  truncated: true,
}

const mount = () => mountPage(<SearchPage />)

describe('SearchPage', () => {
  it('renders, searches and groups results by repository then file', async () => {
    const user = userEvent.setup()
    const calls = stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, ...repositories, 'POST /v1/search': { body: searchBody } })
    mount()
    expect(await screen.findByRole('heading', { name: 'No search yet' })).toBeInTheDocument()
    expect(screen.getByText('All authorized repositories', { selector: 'span' })).toBeInTheDocument()

    await user.type(screen.getByRole('searchbox', { name: 'Search code' }), 'New')
    await user.click(screen.getByRole('button', { name: 'Search' }))

    expect(await screen.findByRole('heading', { name: '3 matches' })).toBeInTheDocument()
    expect(screen.getByText('2 repositories')).toBeInTheDocument()
    expect(await screen.findByText('3 matches in 2 repositories; results were truncated.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'acme/one · 2 matches · 7fd1a60' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'acme/two · 1 match · 7fd1a60' })).toBeInTheDocument()
    const files = screen.getAllByRole('article')
    expect(files).toHaveLength(2)
    expect(within(files[0]).getByText('10')).toBeInTheDocument()
    expect(within(files[0]).getByText('20')).toBeInTheDocument()
    expect(calls.find((call) => call.path === '/v1/search')?.body).toEqual({ query: 'New' })
  })

  it('sends only the checked repositories and prefixes the language', async () => {
    const user = userEvent.setup()
    const calls = stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, ...repositories, 'POST /v1/search': { body: { matches: [], truncated: false } } })
    mount()
    await user.click(await screen.findByRole('button', { name: 'All repositories' }))
    await user.click(screen.getByLabelText('All authorized repositories'))
    expect(screen.getByLabelText('acme/one')).toBeEnabled()
    await user.click(screen.getByLabelText('acme/two'))
    expect(screen.getByRole('button', { name: '1 repository' })).toBeInTheDocument()
    await user.type(screen.getByRole('searchbox', { name: 'Search code' }), 'lang:go Token')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    expect(await screen.findByText('No matches. Try file:.go NewService')).toBeInTheDocument()
    expect(calls.find((call) => call.path === '/v1/search')?.body).toEqual({ query: 'lang:go Token', repositories: ['acme/two'] })
  })

  it('refuses an empty repository subset without calling the API', async () => {
    const user = userEvent.setup()
    const calls = stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, ...repositories })
    mount()
    await user.click(await screen.findByRole('button', { name: 'All repositories' }))
    await user.click(screen.getByLabelText('All authorized repositories'))
    await user.type(screen.getByRole('searchbox', { name: 'Search code' }), 'x')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    expect(await screen.findByText('Choose at least one repository.')).toBeInTheDocument()
    expect(calls.some((call) => call.path === '/v1/search')).toBe(false)
  })

  it('pages the repository picker with the cursor and keeps selections', async () => {
    const user = userEvent.setup()
    const calls = stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      ...session,
      'GET /v1/repositories': { body: { repositories: [repositoryFixture('acme/one', 1)], truncated: true, next_cursor: 'c 1' } },
      'GET /v1/repositories?cursor=c%201': { body: { repositories: [repositoryFixture('acme/one', 1), repositoryFixture('acme/two', 2)], truncated: false } },
    })
    mount()
    await user.click(await screen.findByRole('button', { name: 'All repositories' }))
    expect(await screen.findByText('Showing the first 1 authorized repositories.')).toBeInTheDocument()
    await user.click(screen.getByLabelText('All authorized repositories'))
    await user.click(screen.getByLabelText('acme/one'))
    await user.click(screen.getByRole('button', { name: 'Load more repositories' }))
    expect(await screen.findByLabelText('acme/two')).toBeInTheDocument()
    expect(screen.getAllByLabelText('acme/one')).toHaveLength(1)
    expect(screen.getByLabelText('acme/one')).toBeChecked()
    expect(screen.queryByRole('button', { name: 'Load more repositories' })).not.toBeInTheDocument()
    expect(calls.filter((call) => call.path.startsWith('/v1/repositories'))).toHaveLength(2)
  })

  it('makes paths non-interactive in static mode and keeps only the outbound link', async () => {
    const user = userEvent.setup()
    stubFetch({ 'GET /v1/auth/config': { body: { ...authConfig, file_reads: false } }, ...session, ...repositories, 'POST /v1/search': { body: searchBody } })
    mount()
    await user.type(await screen.findByRole('searchbox', { name: 'Search code' }), 'New')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    const file = (await screen.findAllByRole('article'))[1]
    expect(within(file).getByRole('heading', { level: 3, name: 'a b/README.md' })).toBeInTheDocument()
    expect(within(file).queryByRole('button')).not.toBeInTheDocument()
    const link = within(file).getByRole('link', { name: 'Open indexed source' })
    expect(link).toHaveAttribute('href', `https://github.com/acme/two/blob/${sha}/a%20b/README.md#L1`)
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('renders paths as buttons when file reads are enabled', async () => {
    const user = userEvent.setup()
    stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, ...repositories, 'POST /v1/search': { body: searchBody } })
    mount()
    await user.type(await screen.findByRole('searchbox', { name: 'Search code' }), 'New')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    expect(await screen.findByRole('button', { name: 'src/main.go' })).toBeInTheDocument()
  })

  it('shows a structured API error without clearing the query, with Retry only when retryable', async () => {
    const user = userEvent.setup()
    let attempts = 0
    stubFetch({
      'GET /v1/auth/config': { body: authConfig },
      ...session,
      ...repositories,
      'POST /v1/search': () => {
        attempts++
        return attempts === 1
          ? { status: 503, body: { error: { code: 'unavailable', message: 'Index is busy.', request_id: 'req-9', retryable: true } } }
          : { body: { matches: [], truncated: false } }
      },
    })
    mount()
    const input = await screen.findByRole('searchbox', { name: 'Search code' })
    await user.type(input, 'lang:go Foo')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Index is busy.')
    expect(alert).toHaveTextContent('Request ID: req-9')
    expect(input).toHaveValue('lang:go Foo')
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('No matches. Try file:.go NewService')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('submits on Ctrl+Enter and ignores an empty query', async () => {
    const user = userEvent.setup()
    const calls = stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, ...repositories, 'POST /v1/search': { body: { matches: [], truncated: false } } })
    mount()
    const input = await screen.findByRole('searchbox', { name: 'Search code' })
    await user.click(screen.getByRole('button', { name: 'Search' }))
    expect(calls.some((call) => call.path === '/v1/search')).toBe(false)
    await user.type(input, 'abc{Control>}{Enter}{/Control}')
    await waitFor(() => expect(calls.some((call) => call.path === '/v1/search')).toBe(true))
  })

  it('fills the query from an example without submitting', async () => {
    const user = userEvent.setup()
    const calls = stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, ...repositories })
    mount()
    await user.click(await screen.findByRole('button', { name: 'case:yes repo:payments Token' }))
    expect(screen.getByRole('searchbox', { name: 'Search code' })).toHaveValue('case:yes repo:payments Token')
    expect(calls.some((call) => call.path === '/v1/search')).toBe(false)
  })

  it('opens the syntax drawer with the help text, closes on Escape and restores focus to the opener', async () => {
    const user = userEvent.setup()
    stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, ...repositories })
    mount()
    const toggle = await screen.findByRole('button', { name: 'Query syntax' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await user.click(toggle)
    const drawer = await screen.findByRole('dialog', { name: 'Query syntax' })
    expect(within(drawer).getByText('file:\\.go$')).toBeInTheDocument()
    expect(within(drawer).getByText('"exact phrase"')).toBeInTheDocument()
    expect(within(drawer).getByText('New(Service|Client)')).toBeInTheDocument()
    expect(within(drawer).getByText(/Queries use Zoekt syntax\. Regular expressions are enabled by default\. Combine filters with spaces\. Prefix a term with - to negate it\./)).toBeInTheDocument()
    expect(within(drawer).getByRole('button', { name: 'Close syntax' })).toHaveFocus()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(toggle).toHaveFocus()
  })

  it('focuses the query with the / shortcut outside inputs', async () => {
    const user = userEvent.setup()
    stubFetch({ 'GET /v1/auth/config': { body: authConfig }, ...session, ...repositories })
    mount()
    const input = await screen.findByRole('searchbox', { name: 'Search code' })
    await user.click(screen.getByRole('heading', { name: 'No search yet' }))
    input.blur()
    await user.keyboard('/')
    expect(input).toHaveFocus()
    expect(input).toHaveValue('')
  })
})
