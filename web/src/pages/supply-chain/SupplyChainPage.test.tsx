import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  S,
  collectionsFixture,
  currentStatusFixture,
  evidenceFixture,
  facetsFixture,
  failure,
  mountSupplyChain,
  overviewFixture,
  portfolioDetail,
  portfolioPage1,
  portfolioPage2,
  repositoriesFixture,
  repositoryComponentsPage1,
  repositoryComponentsPage2,
  statusFixture,
} from '@/test/supply-chain-harness'

afterEach(() => vi.restoreAllMocks())

describe('overview', () => {
  const routes = { [`GET /v1/supply-chain/overview?${S}`]: { body: overviewFixture } }

  it('shows every card with its denominator, the assessments in stable order and the denominators verbatim', async () => {
    mountSupplyChain('/supply-chain', routes)
    expect(await screen.findByText('Authorized repositories')).toBeInTheDocument()
    const repositories = screen.getByRole('group', { name: 'Repositories' })
    expect(within(repositories).getByText('9 of 12 authorized repositories')).toBeInTheDocument()
    expect(within(repositories).getByText('repositories this token can read')).toBeInTheDocument()
    const components = screen.getByRole('group', { name: 'Components' })
    expect(within(components).getByText('410')).toBeInTheDocument()
    expect(within(components).getByText('7 of 410 occurrences')).toBeInTheDocument()

    const assessments = screen.getByText('Assessments', { selector: '[data-slot="card-title"]' }).closest('[data-slot="card"]') as HTMLElement
    expect(within(assessments).getAllByText(/^(resolved|conflict|unassessed)$/).map((node) => node.textContent)).toEqual(['Assessment: resolved', 'Assessment: conflict', 'Assessment: unassessed'])
    expect(within(assessments).getByText('4')).toBeInTheDocument()
    expect(screen.getByText('npm')).toBeInTheDocument()
    expect(screen.getByText(/^Oldest: /)).toBeInTheDocument()
    expect(screen.getByText('Parser warnings: 5')).toBeInTheDocument()
    for (const line of overviewFixture.denominators) expect(screen.getByText(line)).toBeInTheDocument()
    expect(screen.queryByText(/%|compliant/)).not.toBeInTheDocument()
  })

  it('says when no repository is in scope', async () => {
    mountSupplyChain('/supply-chain', {
      [`GET /v1/supply-chain/overview?${S}`]: { body: { ...overviewFixture, repositories: { ...overviewFixture.repositories, authorized: 0 }, denominators: [], ecosystems: [], components: { ...overviewFixture.components, assessments: {}, unassessed: 0 } } },
    })
    expect(await screen.findByText('No authorized repositories are in scope')).toBeInTheDocument()
    expect(screen.getByText('No assessments have been recorded.')).toBeInTheDocument()
    expect(screen.getByText('No ecosystems have been observed.')).toBeInTheDocument()
    expect(screen.getByText('No denominators were reported.')).toBeInTheDocument()
  })

  it('shows the disabled-module state when the overview answers 404', async () => {
    mountSupplyChain('/supply-chain', { [`GET /v1/supply-chain/overview?${S}`]: failure(404, 'Not found.', 'not_found') })
    expect(await screen.findByText('Dependencies & Licenses is not available')).toBeInTheDocument()
    expect(screen.getByText('Not found.')).toBeInTheDocument()
    expect(screen.getByText(/GRAPHNEST_SUPPLY_CHAIN=true/)).toBeInTheDocument()
    expect(screen.queryByText('Authorized repositories')).not.toBeInTheDocument()
  })

  it('shows other failures as an alert with Retry', async () => {
    mountSupplyChain('/supply-chain', { [`GET /v1/supply-chain/overview?${S}`]: failure(503, 'Try later.') })
    expect(await screen.findByText('Try later.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })
})

describe('components', () => {
  const list = `/v1/supply-chain/components?${S}&limit=100`
  const routes = {
    [`GET /v1/supply-chain/facets?${S}`]: { body: facetsFixture },
    [`GET ${list}`]: { body: portfolioPage1 },
    [`GET ${list}&cursor=p2`]: { body: portfolioPage2 },
    [`GET ${list}&ecosystem=maven`]: { body: { ...portfolioPage2, components: [], truncated: false } },
    [`GET ${list}&q=lodash`]: { body: { ...portfolioPage2, components: [], truncated: false } },
    [`GET /v1/supply-chain/components/${encodeURIComponent('npm||pkg-1|1.1.0')}?${S}`]: { body: portfolioDetail },
  }

  it('renders the table with text-only cells and the scope indicator', async () => {
    mountSupplyChain('/supply-chain/components', routes)
    const row = await screen.findByRole('row', { name: /@acme\/<i>x<\/i>/ })
    expect(screen.getAllByRole('columnheader').map((header) => header.textContent)).toEqual(['Package', 'Version', 'Ecosystem', 'Assessment', 'Repositories', 'Occurrences', 'Declared', 'Newest observation'])
    expect(within(row).getByText('mixed')).toBeInTheDocument()
    expect(within(row).getByText('NOASSERTION · MIT')).toBeInTheDocument()
    expect(within(row).getByText(/ago$/)).toBeInTheDocument()
    expect(document.querySelector('i')).toBeNull()
    expect(screen.getByText('9 repositories in scope')).toBeInTheDocument()
  })

  it('resets the cursor and refetches when a facet filter changes', async () => {
    const user = userEvent.setup()
    const { calls } = mountSupplyChain('/supply-chain/components', routes)
    await screen.findByRole('row', { name: /@acme/ })
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByRole('row', { name: /second/ })).toBeInTheDocument()
    expect(calls.map((call) => call.path)).toContain(`${list}&cursor=p2`)
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: 'Ecosystem' }))
    await user.click(await screen.findByRole('option', { name: 'maven (80)' }))
    expect(await screen.findByText('No components match these filters')).toBeInTheDocument()
    const filtered = calls.filter((call) => call.path.includes('ecosystem=maven'))
    expect(filtered.map((call) => call.path)).toEqual([`${list}&ecosystem=maven`])
    expect(screen.queryByRole('row', { name: /second/ })).not.toBeInTheDocument()
  })

  it('searches by substring after the debounce and restarts from the first page', async () => {
    const user = userEvent.setup()
    const { calls } = mountSupplyChain('/supply-chain/components', routes)
    await screen.findByRole('row', { name: /@acme/ })
    await user.type(screen.getByRole('searchbox', { name: 'Search name or purl' }), 'lodash')
    await waitFor(() => expect(calls.map((call) => call.path)).toContain(`${list}&q=lodash`))
    expect(calls.map((call) => call.path).filter((path) => path.includes('q='))).toEqual([`${list}&q=lodash`])
    expect(await screen.findByText('No components match these filters')).toBeInTheDocument()
  })

  it('silently restarts from the first page when the server rejects the cursor', async () => {
    const user = userEvent.setup()
    mountSupplyChain('/supply-chain/components', { ...routes, [`GET ${list}&cursor=p2`]: failure(400, 'cursor does not match the filters', 'invalid_cursor') })
    await screen.findByRole('row', { name: /@acme/ })
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByRole('row', { name: /@acme/ })).toBeInTheDocument()
    expect(screen.queryByText('cursor does not match the filters')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Load more' })).toBeInTheDocument()
  })

  it('opens the detail drawer with the occurrences, notes, truncation notice and evidence links', async () => {
    const user = userEvent.setup()
    mountSupplyChain('/supply-chain/components', routes)
    await user.click(await screen.findByRole('button', { name: '@acme/<i>x</i>' }))
    const drawer = await screen.findByRole('dialog')
    expect(await within(drawer).findByText('Component occurrences · <i>x</i>')).toBeInTheDocument()
    expect(within(drawer).getByText("Occurrences are limited to the caller's authorized repositories.")).toBeInTheDocument()
    expect(within(drawer).getByText('Authorized repositories using this component')).toBeInTheDocument()
    const first = within(drawer).getByRole('row', { name: /acme\/widgets/ })
    expect(within(first).getByText('#11')).toBeInTheDocument()
    expect(within(first).getByText('SPDXRef-1')).toBeInTheDocument()
    expect(within(first).getByText('yes')).toBeInTheDocument()
    expect(within(first).getByText('conflict')).toBeInTheDocument()
    const second = within(drawer).getByRole('row', { name: /acme\/gadgets/ })
    expect(within(second).getAllByText('MIT')).toHaveLength(2)
    expect(within(second).getByText('no')).toBeInTheDocument()
    expect(within(drawer).getByText('Occurrences are limited to authorized repositories.')).toBeInTheDocument()
    expect(within(drawer).getByText('Occurrence list is truncated; not every row is shown.')).toBeInTheDocument()
    expect(within(drawer).getByText('pkg:npm/@acme/x@1.1.0')).toBeInTheDocument()
    expect(screen.getByLabelText('location')).toHaveTextContent('key=')

    await user.click(within(first).getByRole('button', { name: /^Evidence/ }))
    expect(screen.getByLabelText('location')).toHaveTextContent('/supply-chain/repositories?repo=101&stream=github%3Asource&element=SPDXRef-1&snapshot=11')
  })

  it('closes the drawer with Escape', async () => {
    const user = userEvent.setup()
    mountSupplyChain('/supply-chain/components', routes)
    await user.click(await screen.findByRole('button', { name: '@acme/<i>x</i>' }))
    await screen.findByRole('dialog')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByLabelText('location')).not.toHaveTextContent('key=')
  })

  it('shows the disabled-module state', async () => {
    mountSupplyChain('/supply-chain/components', { [`GET ${list}`]: failure(404, 'Not found.', 'not_found'), [`GET /v1/supply-chain/facets?${S}`]: failure(404, 'Not found.', 'not_found') })
    expect(await screen.findByText('Dependencies & Licenses is not available')).toBeInTheDocument()
  })
})

describe('repositories', () => {
  const base = '/v1/supply-chain/repositories'
  const routes = {
    'GET /v1/repositories': { body: repositoriesFixture },
    [`GET ${base}/101?${S}`]: { body: statusFixture },
    [`GET ${base}/202?${S}`]: { body: currentStatusFixture },
    [`GET ${base}/101/components?${S}&limit=100`]: { body: repositoryComponentsPage1 },
    [`GET ${base}/101/components?${S}&limit=100&cursor=c2`]: { body: repositoryComponentsPage2 },
    [`GET ${base}/101/collections?${S}`]: { body: collectionsFixture },
    [`GET ${base}/101/component?element=SPDXRef-1&${S}&snapshot_id=11`]: { body: evidenceFixture },
  }

  it('lists each repository with its collection state, freshness, counts and assessment chips', async () => {
    mountSupplyChain('/supply-chain/repositories', routes)
    const widgets = await screen.findByRole('row', { name: /acme\/widgets/ })
    expect(await within(widgets).findByText('failed')).toBeInTheDocument()
    expect(within(widgets).getByText('failed')).toHaveAttribute('data-tone', 'err')
    expect(within(widgets).getByText('2 hours ago')).toBeInTheDocument()
    expect(within(widgets).getByText('42')).toBeInTheDocument()
    expect(within(widgets).getByText('resolved')).toHaveAttribute('data-tone', 'ok')
    expect(within(widgets).getByText('conflict')).toHaveAttribute('data-tone', 'err')
    expect(within(widgets).getByText('unknown')).toHaveAttribute('data-tone', 'unknown')
    const gadgets = screen.getByRole('row', { name: /acme\/gadgets/ })
    expect(await within(gadgets).findByText('current')).toHaveAttribute('data-tone', 'ok')
    expect(within(gadgets).getByText('1 day ago')).toBeInTheDocument()
    expect(within(gadgets).getByText('no assessments')).toBeInTheDocument()
  })

  it('inspects a repository: notice, observation, warnings, components, collections', async () => {
    const user = userEvent.setup()
    mountSupplyChain('/supply-chain/repositories', routes)
    await user.click(await screen.findByRole('button', { name: 'Inspect acme/widgets' }))
    const detail = await screen.findByRole('region', { name: 'Inventory of acme/widgets' })
    expect(await within(detail).findByText('The inventory shown is the last successful observation; the most recent refresh failed.')).toBeInTheDocument()
    expect(within(detail).getByText('Last attempt: forbidden · HTTP 403 · dependency graph is disabled')).toBeInTheDocument()
    expect(within(detail).getByText('GitHub dependency graph exports are observations.')).toBeInTheDocument()
    expect(within(detail).getByText('3 resolved · 1 conflict · 2 unknown')).toBeInTheDocument()
    expect(within(detail).getByText('unknown — not bound to a commit')).toBeInTheDocument()
    expect(within(detail).getByText('configured for npm, maven')).toBeInTheDocument()
    expect(within(detail).getByText(`sha256 ${'ab'.repeat(32)} · spdx-2.3-json · 1234 bytes`)).toBeInTheDocument()
    expect(within(detail).getByText('Warnings (2)')).toBeInTheDocument()
    expect(within(detail).getByText('missing_version · SPDXRef-2 · no version')).toBeInTheDocument()
    expect(within(detail).getByText('unresolved_relationship · SPDXRef-3 · —')).toBeInTheDocument()
    const row = await within(detail).findByRole('row', { name: /<b>x<\/b>/ })
    expect(within(row).getAllByText('—').length).toBeGreaterThanOrEqual(3)
    expect(within(row).getByText('yes')).toBeInTheDocument()
    expect(document.querySelector('b')).toBeNull()
    expect(within(detail).getByText('Showing 1 of snapshot #11')).toBeInTheDocument()
    await user.click(within(detail).getByRole('button', { name: 'Load more' }))
    expect(await within(detail).findByRole('row', { name: /second/ })).toBeInTheDocument()
    const attempts = within(detail).getByRole('row', { name: /projection failed/ })
    expect(within(attempts).getByText('published')).toHaveAttribute('data-tone', 'ok')
    expect(within(detail).getByRole('row', { name: /dependency graph is disabled/ })).toBeInTheDocument()
  })

  it('opens the evidence detail for a component occurrence', async () => {
    const user = userEvent.setup()
    mountSupplyChain('/supply-chain/repositories?repo=101', routes)
    await user.click(await screen.findByRole('button', { name: '<b>x</b>' }))
    const sheet = await screen.findByRole('dialog')
    expect(await within(sheet).findByText('Component evidence · pkg-1')).toBeInTheDocument()
    expect(within(sheet).getByText('declared and registry disagree')).toBeInTheDocument()
    const fingerprint = within(sheet).getByText('abcdef012345')
    expect(fingerprint).toHaveAttribute('title', 'abcdef0123456789'.repeat(4))
    expect(within(sheet).getByText('<b>MIT</b>')).toBeInTheDocument()
    expect(within(sheet).getByText('resolver v1 · SPDX list 3.27.0')).toBeInTheDocument()
    expect(within(sheet).getByText('unavailable')).toHaveAttribute('data-tone', 'warn')
    const link = within(sheet).getByRole('link', { name: 'https://example.com/LICENSE' })
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    expect(link).toHaveAttribute('target', '_blank')
    expect(within(sheet).getByText('SPDXRef-root → DEPENDS_ON → SPDXRef-1')).toBeInTheDocument()
    expect(within(sheet).getByText('SPDXRef-1 → DEPENDS_ON → SPDXRef-gone (unresolved)')).toBeInTheDocument()
    expect(within(sheet).getByText('Evidence is not approval.')).toBeInTheDocument()
    expect(within(sheet).getByText('Evidence list is truncated; not every row is shown.')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('queues a refresh and follows the job to its end', async () => {
    const user = userEvent.setup()
    mountSupplyChain('/supply-chain/repositories', {
      ...routes,
      [`POST ${base}/101/refresh?${S}`]: { status: 202, body: { job: { id: 9, repository_id: 101, state: 'queued', attempt: 0, max_attempts: 3 }, created: true } },
      'GET /v1/supply-chain/jobs/9': { body: { id: 9, repository_id: 101, state: 'succeeded', attempt: 1, max_attempts: 3 } },
    })
    await user.click(await screen.findByRole('button', { name: 'Refresh now acme/widgets' }))
    expect(await screen.findByText('Refresh job #9 is succeeded.')).toBeInTheDocument()
  })

  it('tells a non-administrator that refreshing needs administrator access', async () => {
    const user = userEvent.setup()
    mountSupplyChain('/supply-chain/repositories', { ...routes, [`POST ${base}/101/refresh?${S}`]: failure(403, 'forbidden', 'forbidden') })
    await user.click(await screen.findByRole('button', { name: 'Refresh now acme/widgets' }))
    expect(await screen.findByText('administrator access required')).toBeInTheDocument()
  })

  it('downloads the original document, the CSV and the derived SPDX with the server filename or the fallback', async () => {
    const user = userEvent.setup()
    const downloads: string[] = []
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: () => 'blob:x', revokeObjectURL: () => {} }))
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      downloads.push(this.download)
    })
    const { calls } = mountSupplyChain('/supply-chain/repositories?repo=101', {
      ...routes,
      'GET /v1/supply-chain/snapshots/11/document': { body: {} },
      [`GET /v1/supply-chain/exports/101/components.csv?${S}&snapshot_id=11`]: { body: {}, headers: { 'Content-Disposition': 'attachment; filename="graphnest-components-11.csv"' } },
      [`GET /v1/supply-chain/exports/101/derived.spdx.json?${S}&snapshot_id=11`]: { body: {} },
    })
    await screen.findByRole('button', { name: '<b>x</b>' })
    await user.click(screen.getByRole('button', { name: 'Download original document' }))
    expect(await screen.findByText('Original document downloaded.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Download CSV' }))
    expect(await screen.findByText('Component CSV downloaded.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Download derived SPDX' }))
    expect(await screen.findByText('Derived SPDX document downloaded.')).toBeInTheDocument()
    expect(downloads).toEqual(['graphnest-sbom-snapshot11.json', 'graphnest-components-11.csv', 'graphnest-derived-snapshot11.spdx.json'])
    expect(calls.some((call) => call.path === '/v1/supply-chain/snapshots/11/document')).toBe(true)
  })

  it('explains an empty inventory', async () => {
    mountSupplyChain('/supply-chain/repositories?repo=101', {
      ...routes,
      [`GET ${base}/101/components?${S}&limit=100`]: failure(404, 'no inventory', 'no_inventory'),
    })
    expect(await screen.findByText('No inventory has been collected yet')).toBeInTheDocument()
  })
})

describe('compare', () => {
  const base = '/v1/supply-chain/repositories/101/snapshots'
  const snapshots = { snapshots: [{ id: 11, collected_at: '2026-02-01T10:00:00Z' }, { id: 10, collected_at: '2026-01-25T10:00:00Z' }] }
  const comparison = {
    repository_id: 101,
    base: { id: 10, collected_at: '2026-01-25T10:00:00Z' },
    head: { id: 11, collected_at: '2026-02-01T10:00:00Z' },
    added_components: ['npm:pkg-5@1.5.0'],
    removed_components: ['npm:pkg-0@1.0.0'],
    license_changes: [{ component: 'npm:pkg-1@1.1.0', from: 'MIT', to: 'Apache-2.0' }],
    edges_added: 4,
    edges_removed: 2,
    metadata_changes: ['producer tool changed'],
    notes: ['Comparison is by coordinate, not by SPDXID.'],
  }

  it('compares the newest snapshot with a chosen base', async () => {
    const user = userEvent.setup()
    const { calls } = mountSupplyChain('/supply-chain/compare?repo=101', {
      'GET /v1/repositories': { body: repositoriesFixture },
      [`GET ${base}?${S}&limit=20`]: { body: snapshots },
      'GET /v1/supply-chain/compare?repository_id=101&base=10&head=11': { body: comparison },
    })
    const picker = await screen.findByRole('combobox', { name: 'Compare with previous snapshot' })
    await waitFor(() => expect(picker).toBeEnabled())
    await user.click(picker)
    await user.click(await screen.findByRole('option', { name: /^#10 · / }))
    expect(await screen.findByText(/^Base #10 · .* → head #11 · /)).toBeInTheDocument()
    expect(screen.getByText('npm:pkg-5@1.5.0')).toBeInTheDocument()
    expect(screen.getByText('npm:pkg-0@1.0.0')).toBeInTheDocument()
    expect(screen.getByText('npm:pkg-1@1.1.0: MIT → Apache-2.0')).toBeInTheDocument()
    expect(screen.getByText('producer tool changed')).toBeInTheDocument()
    expect(screen.getByText('4 added · 2 removed')).toBeInTheDocument()
    expect(screen.getByText('Comparison is by coordinate, not by SPDXID.')).toBeInTheDocument()
    expect(calls.filter((call) => call.path.startsWith('/v1/supply-chain/compare'))).toHaveLength(1)
  })

  it('disables the base picker when only one snapshot exists', async () => {
    mountSupplyChain('/supply-chain/compare?repo=101', {
      'GET /v1/repositories': { body: repositoriesFixture },
      [`GET ${base}?${S}&limit=20`]: { body: { snapshots: [snapshots.snapshots[0]] } },
    })
    const picker = await screen.findByRole('combobox', { name: 'Compare with previous snapshot' })
    await waitFor(() => expect(picker).toHaveTextContent('Only one snapshot has been collected'))
    expect(picker).toBeDisabled()
  })
})

describe('unknown views', () => {
  it('renders not found for an unknown view', async () => {
    mountSupplyChain('/supply-chain/nope')
    expect(await screen.findByText('Page not found')).toBeInTheDocument()
  })
})
