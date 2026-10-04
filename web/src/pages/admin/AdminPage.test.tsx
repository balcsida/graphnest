import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { failure, mountConsole } from '@/test/admin-harness'

const date = (value: string) => new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
const sha = (char: string) => char.repeat(40)

const repo = (github_id: number, name: string, status = 'ready', error_code = '') => ({ github_id, name, default_branch: 'main', status, error_code })
const job = (id: number, state = 'queued', extra: object = {}) => ({
  id,
  repository: 'acme/repo',
  target_ref: '',
  target_sha: sha('a'),
  state,
  error_code: '',
  attempt: 1,
  max_attempts: 3,
  updated_at: '2026-01-01T00:00:00Z',
  ...extra,
})

const confirmDialog = async (user: ReturnType<typeof userEvent.setup>) => {
  const dialog = await screen.findByRole('dialog')
  await user.click(within(dialog).getByRole('button', { name: 'Confirm' }))
}

describe('admin probe and lock', () => {
  it.each([
    [403, 'This account is not an administrator.'],
    [404, 'Administrator API is unavailable in static mode.'],
  ])('locks the console on a %i from the overview probe and keeps the credential', async (status, message) => {
    const { calls } = mountConsole('/admin/repositories', { 'GET /v1/admin/overview': failure(status) }, 'bearer')
    expect(await screen.findByText(message)).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(calls.some((call) => call.path === '/v1/admin/repositories')).toBe(false)
    expect(sessionStorage.getItem('graphnest_token')).toBe('admin')
  })

  it('locks on a 401 from the probe: the gate shows the legacy message and the token is dropped', async () => {
    mountConsole('/admin', { 'GET /v1/admin/overview': failure(401) }, 'bearer')
    expect(await screen.findByText('Token required or expired.')).toBeInTheDocument()
    expect(screen.getByLabelText('Bearer token')).toBeInTheDocument()
    expect(sessionStorage.getItem('graphnest_token')).toBeNull()
  })

  it.each([403, 404])('does not lock on an action %i: the error is shown and the console stays open', async (status) => {
    const user = userEvent.setup()
    mountConsole(
      '/admin/repositories',
      {
        'GET /v1/admin/repositories': { body: { repositories: [repo(8, 'acme/failed', 'failed', 'clone_failed')], truncated: false } },
        'POST /v1/admin/repositories/8/reindex': failure(status, `Action refused (${status}).`),
      },
      'bearer',
    )
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    await confirmDialog(user)
    expect(await screen.findByText(`Action refused (${status}).`)).toBeInTheDocument()
    expect(screen.getByRole('row', { name: /acme\/failed/ })).toBeInTheDocument()
    expect(screen.queryByText('This account is not an administrator.')).not.toBeInTheDocument()
    expect(screen.queryByText('Administrator API is unavailable in static mode.')).not.toBeInTheDocument()
    expect(sessionStorage.getItem('graphnest_token')).toBe('admin')
  })

  it('shows a non-lock probe failure as an ordinary error', async () => {
    mountConsole('/admin', { 'GET /v1/admin/overview': failure(503, 'Database unavailable.') })
    expect(await screen.findByText('Database unavailable.')).toBeInTheDocument()
  })
})

describe('session-only screens', () => {
  it.each(['users', 'groups', 'audit'])('refuses to render %s in bearer mode and never requests it', async (section) => {
    const { calls } = mountConsole(`/admin/${section}`, {}, 'bearer')
    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /^(Users|Groups|Audit events)$/ })).not.toBeInTheDocument()
    expect(calls.some((call) => /\/v1\/admin\/(users|groups|audit-events)/.test(call.path))).toBe(false)
  })

  it('renders them in a browser session', async () => {
    mountConsole('/admin/users', { 'GET /v1/admin/users': { body: { users: [], truncated: false } } })
    expect(await screen.findByRole('heading', { name: 'Users' })).toBeInTheDocument()
    expect(await screen.findByText('No users are available.')).toBeInTheDocument()
  })

  it('renders an unknown section as not found', async () => {
    mountConsole('/admin/nope')
    expect(await screen.findByText('Page not found')).toBeInTheDocument()
  })
})

describe('repositories', () => {
  it('reports server totals in the chips and flags a truncated page without a cursor', async () => {
    mountConsole('/admin/repositories', {
      'GET /v1/admin/repositories': { body: { repositories: [repo(7, 'acme/repo', 'mystery'), repo(8, 'acme/failed', 'failed', 'clone_failed')], truncated: true } },
    })
    await screen.findByRole('row', { name: /acme\/failed/ })
    const chips = screen.getAllByRole('button', { pressed: false }).concat(screen.getAllByRole('button', { pressed: true }))
    expect(chips.map((chip) => chip.textContent).filter((text) => text?.includes(' · '))).toEqual(expect.arrayContaining(['All · 320', 'ready · 319', 'failed · 1', 'mystery · 0']))
    expect(screen.getByText('Partial inventory: repositories reached the server result limit.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more repositories' })).not.toBeInTheDocument()
    const failed = screen.getByRole('row', { name: /acme\/failed/ })
    expect(within(failed).getByText('clone_failed')).toBeInTheDocument()
    expect(within(failed).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    const mystery = screen.getByRole('row', { name: /acme\/repo/ })
    expect(within(mystery).getByRole('button', { name: 'Reindex' })).toBeInTheDocument()
    expect(within(mystery).getAllByText('—')).toHaveLength(4)
  })

  it('follows the cursor, drops duplicate repositories and updates the shown count', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/admin/repositories', {
      'GET /v1/admin/repositories': {
        body: { repositories: [0, 1, 2].map((i) => repo(100 + i, `acme/repo-${i}`)), truncated: true, next_cursor: 'repos-page-2' },
      },
      'GET /v1/admin/repositories?cursor=repos-page-2': { body: { repositories: [repo(100, 'acme/repo-0'), repo(200, 'beta/failed', 'failed', 'index_failed')], truncated: false } },
    })
    expect(await screen.findByText('Showing 3 of 320 repositories.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Load more repositories' }))
    await screen.findByRole('row', { name: /beta\/failed/ })
    expect(calls.some((call) => call.path === '/v1/admin/repositories?cursor=repos-page-2')).toBe(true)
    // Header row plus four distinct repositories.
    expect(screen.getAllByRole('row')).toHaveLength(5)
    expect(screen.queryByRole('button', { name: 'Load more repositories' })).not.toBeInTheDocument()
    expect(screen.queryByText(/^Showing/)).not.toBeInTheDocument()
  })

  it('filters by name and status, selects all visible rows and reindexes them after confirmation', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/admin/repositories', {
      'GET /v1/admin/repositories': { body: { repositories: [repo(1, 'acme/one'), repo(2, 'acme/two'), repo(3, 'beta/three', 'failed')], truncated: false } },
      'POST /v1/admin/repositories/1/reindex': { status: 204 },
      'POST /v1/admin/repositories/2/reindex': { status: 204 },
    })
    await screen.findByRole('row', { name: /beta\/three/ })
    expect(screen.getByRole('button', { name: 'Reindex selected' })).toBeDisabled()

    await user.type(screen.getByLabelText('Filter repositories'), 'acme')
    expect(screen.queryByRole('row', { name: /beta\/three/ })).not.toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: 'Select all visible repositories' }))
    expect(screen.getByRole('checkbox', { name: 'Select acme/two' })).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Reindex selected (2)' }))
    expect(await screen.findByText('Queue 2 selected repositories for reindexing?')).toBeInTheDocument()
    await confirmDialog(user)
    expect(await screen.findByText('Selected repositories queued.')).toBeInTheDocument()
    expect(calls.filter((call) => call.method === 'POST').map((call) => call.path)).toEqual(['/v1/admin/repositories/1/reindex', '/v1/admin/repositories/2/reindex'])

    await user.clear(screen.getByLabelText('Filter repositories'))
    await user.click(screen.getByRole('button', { name: /^failed · 1$/ }))
    expect(screen.queryByRole('row', { name: /acme\/one/ })).not.toBeInTheDocument()
    expect(screen.getByRole('row', { name: /beta\/three/ })).toBeInTheDocument()
  })

  it('asks before reconciling and cancel sends nothing', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/admin/repositories', { 'GET /v1/admin/repositories': { body: { repositories: [], truncated: false } } })
    expect(await screen.findByText('No repositories match this view.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Reconcile GitHub' }))
    expect(await screen.findByText('Reconcile repositories from GitHub now?')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(calls.some((call) => call.method === 'POST')).toBe(false)
  })
})

describe('jobs', () => {
  const firstPage = Array.from({ length: 25 }, (_, index) =>
    job(index + 1, ['queued', 'running', 'succeeded', 'failed', 'superseded'][index % 5], { target_ref: index === 0 ? 'refs/heads/main' : '', error_code: index === 3 ? 'index_failed' : '' }),
  )

  it('loads older jobs with the cursor and drops duplicates', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/admin/jobs', {
      'GET /v1/admin/jobs': { body: { jobs: firstPage, truncated: true, next_cursor: 'page-2' } },
      'GET /v1/admin/jobs?cursor=page-2': { body: { jobs: [24, 26, 27].map((id) => job(id, 'succeeded', { target_sha: sha('b') })), truncated: false } },
    })
    expect(await screen.findByRole('cell', { name: '#25' })).toBeInTheDocument()
    expect(screen.getAllByRole('row')).toHaveLength(26)
    await user.click(screen.getByRole('button', { name: 'Load older jobs' }))
    await screen.findByRole('cell', { name: '#27' })
    expect(calls.some((call) => call.path === '/v1/admin/jobs?cursor=page-2')).toBe(true)
    expect(screen.getAllByRole('row')).toHaveLength(28)
    expect(screen.queryByRole('button', { name: 'Load older jobs' })).not.toBeInTheDocument()
  })

  it('lets a refresh of the first page win over pages loaded before it', async () => {
    const user = userEvent.setup()
    let generation = 0
    const { calls, queryClient } = mountConsole('/admin/jobs', {
      'GET /v1/admin/jobs': () => ({
        body: generation === 0 ? { jobs: [job(1), job(2)], truncated: true, next_cursor: 'stale-page' } : { jobs: [job(101), job(102)], truncated: true, next_cursor: 'fresh-page' },
      }),
      'GET /v1/admin/jobs?cursor=stale-page': { body: { jobs: [job(3)], truncated: false } },
      'GET /v1/admin/jobs?cursor=fresh-page': { body: { jobs: [job(103)], truncated: false } },
    })
    await user.click(await screen.findByRole('button', { name: 'Load older jobs' }))
    await screen.findByRole('cell', { name: '#3' })

    generation = 1
    await queryClient.invalidateQueries({ queryKey: ['admin', 'jobs'] })
    await screen.findByRole('cell', { name: '#101' })
    expect(screen.queryByRole('cell', { name: '#3' })).not.toBeInTheDocument()
    expect(screen.queryByRole('cell', { name: '#1' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Load older jobs' }))
    await screen.findByRole('cell', { name: '#103' })
    expect(calls.at(-1)?.path).toBe('/v1/admin/jobs?cursor=fresh-page')
  })

  it('formats cells, counts states and retries a failed job after confirmation', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/admin/jobs', {
      'GET /v1/admin/jobs': { body: { jobs: firstPage, truncated: false } },
      'POST /v1/admin/jobs/4/retry': { status: 204 },
    })
    const row = await screen.findByRole('row', { name: /#1 / })
    expect(within(row).getByText('refs/heads/main')).toBeInTheDocument()
    expect(within(row).getByText('aaaaaaaa')).toBeInTheDocument()
    expect(within(row).getByText('1 / 3')).toBeInTheDocument()
    expect(within(row).getByText(date('2026-01-01T00:00:00Z'))).toBeInTheDocument()
    expect(within(screen.getByRole('row', { name: /#4 / })).getByText('index_failed')).toBeInTheDocument()
    expect(screen.getByText('failed', { selector: 'p' }).closest('[data-slot="card"]')).toHaveTextContent('5')

    await user.click(within(screen.getByRole('row', { name: /#4 / })).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Retry failed job #4?')).toBeInTheDocument()
    await confirmDialog(user)
    expect(await screen.findByText('Job retry queued.')).toBeInTheDocument()
    expect(calls.some((call) => call.method === 'POST' && call.path === '/v1/admin/jobs/4/retry')).toBe(true)
  })
})

describe('sections', () => {
  it('renders the overview cards and health', async () => {
    mountConsole('/admin')
    expect(await screen.findByText('320')).toBeInTheDocument()
    expect(screen.getByText('Queue depth')).toBeInTheDocument()
    expect(screen.getByText('SCIP indexes')).toBeInTheDocument()
    expect(screen.getByText('Dependency health')).toBeInTheDocument()
    expect(screen.getByText('Webhook deliveries: 1')).toBeInTheDocument()
    expect(screen.getByText('Dependencies: 1')).toBeInTheDocument()
    expect(await screen.findByText('Health OK')).toBeInTheDocument()
    expect(await screen.findByText('Readiness OK')).toBeInTheDocument()
  })

  it('renders webhook deliveries', async () => {
    mountConsole('/admin/deliveries', {
      'GET /v1/admin/webhook-deliveries': {
        body: {
          deliveries: [
            { id: 1, delivery_id: 'delivery-1', event: 'push', state: 'succeeded', installation_id: 1, error_code: '', received_at: '2026-01-01T00:00:00Z', processed_at: '2026-01-02T00:00:00Z' },
            { id: 2, delivery_id: 'delivery-2', event: 'push', state: 'queued', installation_id: 1, error_code: 'rate_limited', received_at: '2026-01-03T00:00:00Z' },
          ],
          truncated: true,
        },
      },
    })
    const done = await screen.findByRole('row', { name: /succeeded/ })
    expect(within(done).getByText('Processed')).toBeInTheDocument()
    expect(within(done).getByText(date('2026-01-02T00:00:00Z'))).toBeInTheDocument()
    const queued = screen.getByRole('row', { name: /rate_limited/ })
    expect(within(queued).getByText('—')).toBeInTheDocument()
    expect(screen.getByText('Partial inventory: webhooks reached the server result limit.')).toBeInTheDocument()
    expect(screen.getByText(/verifies GitHub HMAC signatures/)).toBeInTheDocument()
  })

  it('renders the GitHub App cards, configuration and installations and reconciles', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/admin/github', {
      'GET /v1/admin/github': {
        body: {
          app_id: 42,
          web_url: 'https://github.example',
          api_url: 'https://github.example/api',
          upload_url: 'https://github.example/upload',
          git_url: 'https://github.example',
          api_version: '2022-11-28',
          private_key_configured: true,
          webhook_secret_configured: false,
          ca_configured: false,
          installations: [{ github_id: 9, account_login: 'acme', account_type: 'Organization', status: 'active' }],
          truncated: false,
        },
      },
      'POST /v1/admin/reconcile': { status: 204 },
    })
    expect(await screen.findByText('acme · Organization · active · #9')).toBeInTheDocument()
    expect(screen.getByText('Missing')).toBeInTheDocument()
    expect(screen.getByText('Default trust')).toBeInTheDocument()
    expect(screen.getByText('2022-11-28')).toBeInTheDocument()
    // Configuration URLs are plain text, never links.
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Run now' }))
    await confirmDialog(user)
    expect(await screen.findByText('Reconciliation completed.')).toBeInTheDocument()
    expect(calls.some((call) => call.method === 'POST' && call.path === '/v1/admin/reconcile')).toBe(true)
  })

  it('renders SCIP uploads and dependencies, uploads a file and refreshes from GitHub', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/admin/scip', {
      'GET /v1/admin/scip/uploads': {
        body: { uploads: [{ id: 1, repository_id: 1, repository: 'acme/repo', commit: sha('c'), project_root: '', indexer_name: 'scip-go', indexer_version: '1.2', uploaded_at: '2026-01-01T00:00:00Z' }], truncated: true },
      },
      'GET /v1/admin/scip/dependencies': { body: { dependencies: [], truncated: false } },
      [`POST /v1/scip/uploads?repository_id=5&commit=${sha('d')}`]: { status: 204 },
      'POST /v1/scip/dependencies/github': { body: { available: true, packages: 3 } },
    })
    expect(await screen.findByText('acme/repo · cccccccc · scip-go 1.2')).toBeInTheDocument()
    expect(screen.getByText('No package dependency metadata available.')).toBeInTheDocument()
    expect(screen.getByText('Partial inventory: SCIP indexes reached the server result limit.')).toBeInTheDocument()

    await user.type(screen.getByLabelText('Repository ID', { selector: '#scip-repo' }), '5')
    await user.type(screen.getByLabelText('Indexed commit'), sha('d'))
    await user.upload(screen.getByLabelText('SCIP file'), new File(['data'], 'index.scip', { type: 'application/vnd.scip+protobuf' }))
    // jsdom does not count a user-event upload towards the file input's `required` check, so submit directly.
    fireEvent.submit(screen.getByRole('button', { name: 'Upload' }).closest('form')!)
    expect(await screen.findByText('Upload index.scip for repository 5?')).toBeInTheDocument()
    await confirmDialog(user)
    expect(await screen.findByText('SCIP index uploaded.')).toBeInTheDocument()
    const upload = calls.find((call) => call.method === 'POST' && call.path.startsWith('/v1/scip/uploads'))
    expect(upload?.headers.get('Content-Type')).toBe('application/vnd.scip+protobuf')

    await user.type(screen.getByLabelText('Repository ID', { selector: '#dependency-repo' }), '5')
    await user.click(screen.getByRole('button', { name: 'Refresh dependencies' }))
    expect(await screen.findByText('Replace dependency metadata with GitHub dependency graph data?')).toBeInTheDocument()
    await confirmDialog(user)
    expect(await screen.findByText('GitHub dependencies refreshed.')).toBeInTheDocument()
    expect(calls.find((call) => call.path === '/v1/scip/dependencies/github')?.body).toEqual({ repository_id: 5 })
  })

  it('renders users and edits, suspends and revokes through confirmations', async () => {
    const user = userEvent.setup()
    const ada = {
      id: 7,
      external_id: 'x',
      user_name: 'ada',
      display_name: 'Ada',
      source: 'scim',
      scim_active: true,
      suspended: false,
      administrator: true,
      repository_ids: [101, 102],
      direct_administrator: false,
      direct_repository_ids: [101],
      github_repository_ids: [5],
    }
    const { calls } = mountConsole('/admin/users', {
      'GET /v1/admin/users': { body: { users: [ada, { ...ada, id: 8, user_name: 'bob', suspended: true, direct_repository_ids: [], github_repository_ids: [] }], truncated: true } },
      'POST /v1/admin/users/7/suspend': { status: 204 },
      'POST /v1/admin/users/8/restore': { status: 204 },
      'POST /v1/admin/users/7/revoke-credentials': { status: 204 },
      'PUT /v1/admin/users/7/access': { status: 204 },
    })
    const row = await screen.findByRole('row', { name: /ada/ })
    expect(within(row).getByText('active')).toBeInTheDocument()
    expect(within(row).getByText('Administrator · 101, 102')).toBeInTheDocument()
    expect(within(row).getByText('Standard · 101 · GitHub: 5')).toBeInTheDocument()
    expect(within(screen.getByRole('row', { name: /bob/ })).getByText('Standard · —')).toBeInTheDocument()
    expect(screen.getByText('Partial inventory: users reached the server result limit.')).toBeInTheDocument()

    await user.click(within(row).getByRole('button', { name: 'Suspend user' }))
    expect(await screen.findByText('Suspend ada?')).toBeInTheDocument()
    await confirmDialog(user)
    expect(await screen.findByText('User updated.')).toBeInTheDocument()
    await user.click(within(screen.getByRole('row', { name: /bob/ })).getByRole('button', { name: 'Restore user' }))
    expect(await screen.findByText('Restore bob?')).toBeInTheDocument()
    await confirmDialog(user)
    await user.click(within(row).getByRole('button', { name: 'Revoke credentials' }))
    expect(await screen.findByText('Revoke all credentials for ada?')).toBeInTheDocument()
    await confirmDialog(user)
    expect(await screen.findByText('Credentials revoked.')).toBeInTheDocument()

    await user.click(within(row).getByRole('button', { name: 'Edit access' }))
    expect(screen.getByLabelText('User ID')).toHaveValue('7')
    expect(screen.getByLabelText('User ID')).toHaveFocus()
    expect(screen.getByLabelText('Repository IDs (comma-separated)')).toHaveValue('101')
    await user.click(screen.getByRole('button', { name: 'Save direct access' }))
    expect(await screen.findByText('Replace direct access for this user?')).toBeInTheDocument()
    await confirmDialog(user)
    expect(await screen.findByText('Access replaced.')).toBeInTheDocument()
    expect(calls.find((call) => call.method === 'PUT')?.body).toEqual({ direct_administrator: false, direct_repository_ids: [101] })
    expect(calls.filter((call) => call.method === 'POST').map((call) => call.path)).toEqual([
      '/v1/admin/users/7/suspend',
      '/v1/admin/users/8/restore',
      '/v1/admin/users/7/revoke-credentials',
    ])
  })

  it('rejects malformed repository ids without calling the API', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/admin/groups', {
      'GET /v1/admin/groups': { body: { groups: [{ id: 9, external_id: 'g', display_name: 'Engineering', administrator: true, repository_ids: [101, 102], member_count: 2 }], truncated: false } },
    })
    await user.click(await screen.findByRole('button', { name: 'Edit access' }))
    await user.clear(screen.getByLabelText('Repository IDs (comma-separated)'))
    await user.type(screen.getByLabelText('Repository IDs (comma-separated)'), '1,abc')
    await user.click(screen.getByRole('button', { name: 'Save group access' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Group ID and repository IDs must be positive whole numbers.')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(calls.some((call) => call.method === 'PUT')).toBe(false)
  })

  it('renders groups and replaces group access', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/admin/groups', {
      'GET /v1/admin/groups': { body: { groups: [{ id: 9, external_id: 'g', display_name: 'Engineering', administrator: true, repository_ids: [101, 102], member_count: 2 }], truncated: false } },
      'PUT /v1/admin/groups/9/access': { status: 204 },
    })
    const row = await screen.findByRole('row', { name: /Engineering/ })
    expect(within(row).getByText('2')).toBeInTheDocument()
    expect(within(row).getByText('Administrator · 101, 102')).toBeInTheDocument()
    await user.click(within(row).getByRole('button', { name: 'Edit access' }))
    expect(screen.getByRole('checkbox', { name: 'Administrator' })).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Save group access' }))
    expect(await screen.findByText('Replace direct access for this group?')).toBeInTheDocument()
    await confirmDialog(user)
    await waitFor(() => expect(calls.find((call) => call.method === 'PUT')?.body).toEqual({ administrator: true, repository_ids: [101, 102] }))
  })

  it('renders audit events', async () => {
    mountConsole('/admin/audit', {
      'GET /v1/admin/audit-events': {
        body: {
          events: [
            { actor_type: 'user', actor_id: '7', target_type: 'api_token', target_id: '3', authentication_method: '', operation: 'api_token_created', outcome: 'success', request_id: '', created_at: '2026-01-01T00:00:00Z' },
          ],
          truncated: false,
        },
      },
    })
    const row = await screen.findByRole('row', { name: /api_token_created/ })
    expect(within(row).getByText('user:7')).toBeInTheDocument()
    expect(within(row).getByText('api_token:3')).toBeInTheDocument()
    expect(within(row).getAllByText('—')).toHaveLength(2)
    expect(within(row).getByText('success')).toBeInTheDocument()
  })
})
