import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { S, facetsFixture, graphFixture, mountSupplyChain } from '@/test/supply-chain-harness'

afterEach(() => vi.restoreAllMocks())

const graph = `/v1/supply-chain/graph?${S}&min_repositories=2&limit=150`
const routes = {
  [`GET /v1/supply-chain/facets?${S}`]: { body: facetsFixture },
  [`GET ${graph}`]: { body: graphFixture },
}

describe('dependency graph', () => {
  it('asks the server for the graph once and lists the edges in the table alternative', async () => {
    const user = userEvent.setup()
    const { calls } = mountSupplyChain('/supply-chain/graph', routes)
    expect(await screen.findByRole('region', { name: 'Dependency graph' })).toBeInTheDocument()
    expect(await screen.findByText('Showing 2 of 2 matching dependencies used by 2 repositories.', {}, { timeout: 10000 })).toBeInTheDocument()
    // One aggregate request: the view no longer pages through the component list, reads the overview or lists repositories.
    const requests = calls.map((call) => call.path).filter((path) => path.startsWith('/v1/supply-chain/') || path.startsWith('/v1/repositories'))
    expect(requests.sort()).toEqual([`/v1/supply-chain/facets?${S}`, graph])
    expect(screen.queryByText(/^Each dependency is linked to the first/)).not.toBeInTheDocument()
    expect(screen.getByRole('group', { name: 'Legend' })).toHaveTextContent('npm')

    await user.click(screen.getByRole('switch', { name: 'Show as table' }))
    const table = await screen.findByRole('table')
    expect(within(table).getAllByRole('row')).toHaveLength(1 + 4)
    expect(within(table).getAllByRole('button', { name: 'acme/widgets' })).toHaveLength(2)
    expect(within(table).getAllByRole('button', { name: '@acme/<i>x</i>' })).toHaveLength(2)

    await user.click(within(table).getAllByRole('button', { name: 'second' })[0])
    const sheet = await screen.findByRole('dialog')
    expect(within(sheet).getByText('2.0.0')).toBeInTheDocument()
    expect(within(sheet).getByRole('link', { name: 'Open in the components list' })).toHaveAttribute('href', `/supply-chain/components?${S}&ecosystem=npm&q=second`)
    expect(within(sheet).getByRole('link', { name: 'acme/gadgets' })).toHaveAttribute('href', `/supply-chain/repositories?repo=202&${S}`)
  })

  it('sends the filters and the limits to the server', async () => {
    const filtered = `/v1/supply-chain/graph?${S}&min_repositories=5&limit=25&ecosystem=npm&assessment=conflict&q=pad`
    const { calls } = mountSupplyChain('/supply-chain/graph?min=5&top=25&ecosystem=npm&assessment=conflict&q=pad', { ...routes, [`GET ${filtered}`]: { body: { ...graphFixture, dependencies: [], repositories: [], matching: 0 } } })
    expect(await screen.findAllByText('No shared dependencies match these filters')).not.toHaveLength(0)
    expect(calls.map((call) => call.path)).toContain(filtered)
  })

  it('says so when the server linked only some of the repositories', async () => {
    const user = userEvent.setup()
    const [first, second] = graphFixture.dependencies
    const partial = { ...graphFixture, matching: 4812, truncated: true, dependencies: [{ ...first, repository_count: 1200, version_count: 31 }, second] }
    mountSupplyChain('/supply-chain/graph', { ...routes, [`GET ${graph}`]: { body: partial } })
    expect(await screen.findByText('Showing 2 of 4812 matching dependencies linked to 2 repositories.', {}, { timeout: 10000 })).toBeInTheDocument()
    expect(screen.getByText(/^Each dependency is linked to the first of the repositories using it/)).toBeInTheDocument()

    await user.click(screen.getByRole('switch', { name: 'Show as table' }))
    const table = await screen.findByRole('table')
    expect(within(table).getAllByText('1.1.0 and 30 more')).toHaveLength(2)
    await user.click(within(table).getAllByRole('button', { name: '@acme/<i>x</i>' })[0])
    const sheet = await screen.findByRole('dialog')
    expect(within(sheet).getByRole('heading', { name: 'Repositories using it (1200)' })).toBeInTheDocument()
    expect(within(sheet).getByText('The graph links the first 2; the components list has every occurrence.')).toBeInTheDocument()
  })
})
