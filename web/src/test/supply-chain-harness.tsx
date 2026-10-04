import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { AuthProvider, useAuth } from '@/lib/auth'
import SupplyChainPage from '@/pages/supply-chain/SupplyChainPage'
import { authConfig, stubFetch, type Handler } from '@/test/fetch'

// Test helper file: it exports a mount function and fixtures next to private components.
/* eslint-disable react-refresh/only-export-components */
function Ready({ children }: { children: ReactNode }) {
  return useAuth().status === 'authenticated' ? children : null
}

function Location() {
  const { pathname, search } = useLocation()
  return <output aria-label="location">{pathname + search}</output>
}

export const S = 'stream=github%3Asource'

export const failure = (status: number, message: string, code = 'error') => ({
  status,
  body: { error: { code, message, request_id: 'test', retryable: false } },
})

export const repositoriesFixture = {
  repositories: [
    { id: 1, github_id: 101, name: 'acme/widgets' },
    { id: 2, github_id: 202, name: 'acme/gadgets' },
  ],
  truncated: false,
}

export const overviewFixture = {
  stream: 'github:source',
  generated_at: '2026-02-02T10:00:00Z',
  repositories: { authorized: 12, with_inventory: 9, never_collected: 2, stale: 3, failed_last_attempt: 1, opted_out: 1 },
  components: { occurrences: 410, unique_coordinates: 260, without_purl: 7, without_version: 4, unassessed: 4, assessments: { resolved: 3, conflict: 1 } },
  warning_total: 5,
  oldest_collected_at: '2026-01-20T08:00:00Z',
  newest_collected_at: '2026-02-01T10:00:00Z',
  ecosystems: [
    { value: 'npm', count: 180 },
    { value: 'maven', count: 80 },
  ],
  denominators: [
    'Authorized repositories: repositories this token can read.',
    'With inventory: authorized repositories with at least one snapshot.',
    'Occurrences: component rows across the latest snapshot of each repository.',
    'Unique coordinates: distinct ecosystem, namespace, name and version tuples.',
    'Assessments describe collected evidence; they are not a compliance verdict.',
  ],
}

export const facetsFixture = {
  stream: 'github:source',
  ecosystems: [
    { value: 'npm', count: 180 },
    { value: 'maven', count: 80 },
  ],
  assessments: [
    { value: 'resolved', count: 3 },
    { value: 'conflict', count: 1 },
  ],
  licenses: [
    { value: 'MIT', count: 120 },
    { value: 'Apache-2.0 AND MIT', count: 4 },
  ],
}

const portfolioRow = (key: string, name: string, extra: object = {}) => ({
  key,
  ecosystem: 'npm',
  namespace: '@acme',
  name,
  version: '1.1.0',
  purl: 'pkg:npm/@acme/x@1.1.0',
  repository_count: 2,
  occurrence_count: 3,
  assessment: 'mixed',
  assessment_statuses: ['resolved', 'conflict'],
  expression: '',
  declared_raw: ['NOASSERTION', 'MIT', 'MIT'],
  newest_collected_at: '2026-02-01T10:00:00Z',
  oldest_collected_at: '2026-01-20T08:00:00Z',
  repositories: [{ id: 101 }, { id: 202 }],
  ...extra,
})

export const portfolioPage1 = {
  stream: 'github:source',
  repositories_in_scope: 9,
  components: [portfolioRow('npm||pkg-1|1.1.0', '<i>x</i>')],
  truncated: true,
  next_cursor: 'p2',
}

export const portfolioPage2 = {
  stream: 'github:source',
  repositories_in_scope: 9,
  components: [portfolioRow('npm||pkg-2|2.0.0', 'second', { namespace: undefined, version: '2.0.0', assessment: 'resolved', expression: 'MIT' })],
  truncated: false,
}

export const portfolioDetail = {
  key: 'npm||pkg-1|1.1.0',
  stream: 'github:source',
  ecosystem: 'npm',
  namespace: '@acme',
  name: '<i>x</i>',
  version: '1.1.0',
  truncated: true,
  notes: ['Occurrences are limited to authorized repositories.'],
  occurrences: [
    { repository_id: 101, repository: 'acme/widgets', snapshot_id: 11, collected_at: '2026-02-01T10:00:00Z', element_id: 'SPDXRef-1', root: true, declared_raw: 'NOASSERTION', assessment: 'conflict', expression: '', detail_path: '/v1/supply-chain/repositories/101/component?element=SPDXRef-1' },
    { repository_id: 202, repository: 'acme/gadgets', snapshot_id: 21, collected_at: '2026-01-30T10:00:00Z', element_id: 'SPDXRef-9', root: false, declared_raw: 'MIT', assessment: 'resolved', expression: 'MIT', detail_path: '/v1/supply-chain/repositories/202/component?element=SPDXRef-9' },
  ],
}

const snapshot = {
  id: 11,
  repository_id: 1,
  stream: 'github:source',
  producer: 'github',
  subject: 'source',
  collected_at: '2026-02-01T10:00:00Z',
  created_at_claimed: '2026-02-01T09:00:00Z',
  producer_tool: 'github-dependency-graph',
  component_count: 42,
  warning_count: 2,
  warnings: [
    { code: 'missing_version', element: 'SPDXRef-2', detail: 'no version' },
    { code: 'unresolved_relationship', element: 'SPDXRef-3' },
  ],
}

export const statusFixture = {
  repository_id: 101,
  repository: 'acme/widgets',
  stream: 'github:source',
  producer: 'github',
  subject: 'source',
  collection: 'failed',
  freshness_seconds: 7200,
  latest_snapshot: snapshot,
  last_collection: { id: 5, outcome: 'forbidden', http_status: 403, message: 'dependency graph is disabled', finished_at: '2026-02-02T10:00:00Z' },
  active_job: null,
  enrichment: 'configured',
  enrichment_ecosystems: ['npm', 'maven'],
  license_summary: { resolved: 3, conflict: 1, unknown: 2 },
  opt_out: false,
  notes: ['GitHub dependency graph exports are observations.'],
  documents: [{ snapshot_id: 11, sha256: 'ab'.repeat(32), format: 'spdx-2.3-json', bytes: 1234, path: '/v1/supply-chain/snapshots/11/document' }],
  streams: [],
}

export const currentStatusFixture = { ...statusFixture, repository_id: 202, repository: 'acme/gadgets', collection: 'current', freshness_seconds: 90000, last_collection: null, license_summary: {}, documents: [], latest_snapshot: null, notes: [] }

const component = (element: string, name: string, extra: object = {}) => ({
  element_id: element,
  ordinal: 1,
  name,
  version: '1.0.0',
  purl: `pkg:npm/${name}@1.0.0`,
  ecosystem: 'npm',
  license_declared_raw: 'NOASSERTION',
  license_concluded_raw: null,
  is_root: false,
  scope: 'direct',
  license: { status: 'resolved', expression: 'MIT', evidence_count: 1, assessed_at: '2026-02-01T10:00:00Z', evidence_fingerprint: 'f'.repeat(64) },
  ...extra,
})

export const repositoryComponentsPage1 = {
  snapshot_id: 11,
  components: [component('SPDXRef-1', '<b>x</b>', { version: null, purl: null, license: null, is_root: true, scope: 'root' })],
  truncated: true,
  next_cursor: 'c2',
}
export const repositoryComponentsPage2 = { snapshot_id: 11, components: [component('SPDXRef-2', 'second', { ordinal: 2 })], truncated: false }

export const evidenceFixture = {
  component: { ...component('SPDXRef-1', 'pkg-1'), license: { status: 'conflict', expression: 'MIT', conflict_detail: 'declared and registry disagree', evidence_count: 2, assessed_at: '2026-02-01T10:00:00Z', evidence_fingerprint: 'abcdef0123456789'.repeat(4) } },
  snapshot,
  declarations: [{ id: 0, source: 'producer_declared', ecosystem: 'npm', name: 'pkg-1', version: '1.0.0', raw_value: '<b>MIT</b>', raw_kind: 'expression', parse_status: 'parsed', resolver_version: 1, license_list_version: '3.27.0', fetched_at: '2026-02-01T10:00:00Z', outcome: 'resolved' }],
  evidence: [
    {
      id: 7,
      source: 'registry_npm',
      route: 'registry.npmjs.org',
      ecosystem: 'npm',
      name: 'pkg-1',
      version: '1.0.0',
      raw_value: 'Apache-2.0',
      raw_kind: 'expression',
      parse_status: 'parsed',
      expression: 'Apache-2.0',
      license_url: 'https://example.com/LICENSE',
      license_file_name: 'LICENSE',
      resolver_version: 1,
      license_list_version: '3.27.0',
      fetched_at: '2026-02-01T10:00:00Z',
      outcome: 'unavailable',
      message: 'registry timed out',
    },
  ],
  relationships: [
    { from: 'SPDXRef-root', type: 'DEPENDS_ON', to: 'SPDXRef-1', resolved: true },
    { from: 'SPDXRef-1', type: 'DEPENDS_ON', to: 'SPDXRef-gone', resolved: false },
  ],
  notes: ['Evidence is not approval.'],
  truncated: true,
}

export const collectionsFixture = {
  collections: [
    { id: 5, job_id: null, producer: 'github', stream: 'github:source', started_at: '2026-02-02T09:59:00Z', finished_at: '2026-02-02T10:00:00Z', outcome: 'forbidden', http_status: 403, snapshot_id: null, error_code: 'forbidden', message: 'dependency graph is disabled' },
    { id: 4, job_id: null, producer: 'github', stream: 'github:source', started_at: '2026-02-01T09:59:00Z', finished_at: '2026-02-01T10:00:00Z', outcome: 'published', http_status: 200, snapshot_id: 11, projection_error: 'projection failed' },
  ],
  truncated: false,
}

/** Mounts the supply-chain routes for a signed-in browser session with real auth and query providers. */
export function mountSupplyChain(path: string, routes: Record<string, Handler> = {}) {
  const calls = stubFetch({
    'GET /v1/auth/config': { body: authConfig },
    'GET /v1/auth/session': { body: { method: 'oidc' } },
    ...routes,
  })
  const view = render(
    <QueryClientProvider client={new QueryClient()}>
      <AuthProvider>
        <MemoryRouter initialEntries={[path]}>
          <Ready>
            <Routes>
              <Route path="supply-chain" element={<SupplyChainPage />} />
              <Route path="supply-chain/:view" element={<SupplyChainPage />} />
            </Routes>
            <Location />
          </Ready>
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
  return { ...view, calls }
}
