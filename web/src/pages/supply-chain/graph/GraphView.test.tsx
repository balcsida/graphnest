import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { S, facetsFixture, mountSupplyChain, overviewFixture, portfolioPage1, portfolioPage2, repositoriesFixture } from '@/test/supply-chain-harness'

afterEach(() => vi.restoreAllMocks())

const list = `/v1/supply-chain/components?${S}&limit=100`
const routes = {
  [`GET /v1/supply-chain/overview?${S}`]: { body: overviewFixture },
  [`GET /v1/supply-chain/facets?${S}`]: { body: facetsFixture },
  'GET /v1/repositories': { body: repositoriesFixture },
  [`GET ${list}`]: { body: portfolioPage1 },
  [`GET ${list}&cursor=p2`]: { body: portfolioPage2 },
}

describe('dependency graph', () => {
  it('scans both component pages and lists the edges in the table alternative', async () => {
    const user = userEvent.setup()
    const { calls } = mountSupplyChain('/supply-chain/graph', routes)
    expect(await screen.findByRole('region', { name: 'Dependency graph' })).toBeInTheDocument()
    expect(await screen.findByText('Showing 2 of 2 matching dependencies used by 2 repositories.', {}, { timeout: 4000 })).toBeInTheDocument()
    expect(calls.filter((call) => call.path.startsWith('/v1/supply-chain/components')).map((call) => call.path)).toEqual([list, `${list}&cursor=p2`])
    expect(screen.queryByText(/^Based on the first/)).not.toBeInTheDocument()
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

  it('says so when the scan stops at the page cap', async () => {
    const capped: typeof routes = { ...routes }
    for (let page = 1; page <= 30; page++) {
      const body = { ...portfolioPage1, next_cursor: `p${page + 1}` }
      Object.assign(capped, { [`GET ${list}${page === 1 ? '' : `&cursor=p${page}`}`]: { body } })
    }
    const { calls } = mountSupplyChain('/supply-chain/graph', capped)
    expect(await screen.findByText('Based on the first 30 of 260 unique coordinates; narrow the filters to see the rest.')).toBeInTheDocument()
    await waitFor(() => expect(calls.filter((call) => call.path.startsWith('/v1/supply-chain/components'))).toHaveLength(30))
  })
})
