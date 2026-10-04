import { screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { S, failure, facetsFixture, mountSupplyChain, overviewFixture, repositoriesFixture } from '@/test/supply-chain-harness'

afterEach(() => vi.restoreAllMocks())

const facetsFor = (licenses: { value: string; count: number }[]) => ({ ...facetsFixture, licenses })

const routes = {
  [`GET /v1/supply-chain/overview?${S}`]: { body: overviewFixture },
  [`GET /v1/supply-chain/facets?${S}`]: { body: facetsFixture },
  'GET /v1/repositories': { body: repositoriesFixture },
  [`GET /v1/supply-chain/facets?${S}&repository_id=101`]: { body: facetsFor([{ value: 'MIT', count: 3 }, { value: 'GPL-3.0-only', count: 1 }]) },
  [`GET /v1/supply-chain/facets?${S}&repository_id=202`]: { body: facetsFor([{ value: 'Apache-2.0', count: 2 }]) },
}

describe('license dashboard', () => {
  it('shows the evidence note, the KPI denominators and every chart with its accessible table', async () => {
    mountSupplyChain('/supply-chain/licenses', routes)
    expect(await screen.findByText('Evidence, not compliance')).toBeInTheDocument()
    const kpis = await screen.findByRole('group', { name: 'Key figures' })
    expect(within(kpis).getByText('9 of 12 authorized repositories')).toBeInTheDocument()
    expect(within(kpis).getByText('Assessed share')).toBeInTheDocument()
    expect(within(kpis).getByText('50%')).toBeInTheDocument()
    expect(within(kpis).getByText('4 of 8 coordinates with an assessment status')).toBeInTheDocument()
    for (const label of ['Conflicts', 'Unlicensed', 'Never collected', 'Stale', 'Unique coordinates']) expect(within(kpis).getByText(label)).toBeInTheDocument()

    const status = await screen.findByRole('table', { name: 'Assessment status' })
    expect(within(status).getByText('conflict')).toBeInTheDocument()

    const link = await screen.findByRole('link', { name: 'MIT: show components' })
    expect(link).toHaveAttribute('href', `/supply-chain/components?${S}&license=MIT`)

    const mix = await screen.findByRole('table', { name: 'License families per repository' })
    expect(within(mix).getByRole('row', { name: /acme\/widgets 3 0 1 0 0 MIT \(3\), GPL-3\.0-only \(1\)/ })).toBeInTheDocument()
    expect(within(mix).getByRole('row', { name: /acme\/gadgets 2 0 0 0 0 Apache-2\.0 \(2\)/ })).toBeInTheDocument()

    expect(screen.getByRole('table', { name: 'Components per ecosystem' })).toBeInTheDocument()
    expect(screen.getByText(/^Oldest collection: /)).toBeInTheDocument()
    expect(screen.getByText('Failed last attempt: 1 of 12 authorized repositories')).toBeInTheDocument()
    for (const line of overviewFixture.denominators) expect(screen.getByText(line)).toBeInTheDocument()
  })

  it('narrows the overview and the per-repository reads to the chosen scope', async () => {
    const { calls } = mountSupplyChain('/supply-chain/licenses?repos=202', {
      ...routes,
      [`GET /v1/supply-chain/overview?${S}&repository_id=202`]: { body: overviewFixture },
    })
    const mix = await screen.findByRole('table', { name: 'License families per repository' })
    expect(within(mix).queryByText('acme/widgets')).not.toBeInTheDocument()
    expect(within(mix).getByText('acme/gadgets')).toBeInTheDocument()
    expect(calls.some((call) => call.path === `/v1/supply-chain/facets?${S}`)).toBe(false)
    expect(calls.some((call) => call.path === `/v1/supply-chain/overview?${S}&repository_id=202`)).toBe(true)
    expect(await screen.findByRole('button', { name: /Repository scope/ })).toHaveTextContent('1 selected')
  })

  it('shows the disabled-module state when the overview answers 404', async () => {
    mountSupplyChain('/supply-chain/licenses', { ...routes, [`GET /v1/supply-chain/overview?${S}`]: failure(404, 'Not found.', 'not_found') })
    expect(await screen.findByText('Dependencies & Licenses is not available')).toBeInTheDocument()
  })
})
