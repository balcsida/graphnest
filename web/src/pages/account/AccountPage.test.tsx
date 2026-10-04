import { fireEvent, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { failure, mountConsole } from '@/test/admin-harness'

const date = (value: string) => new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))

const visible = { id: 3, prefix: 'gnp_visible', repository_ids: [101], created_at: '2026-01-01T00:00:00Z', expires_at: '2026-08-29T00:00:00Z' }
const broker = { id: 5, prefix: 'gnp_broker__', delegation_only: true, created_at: '2026-01-02T00:00:00Z' }
const grant = (id: number, client_name: string) => ({ id, client_name, scope: '', created_at: '2026-09-01T00:00:00Z', last_used_at: '2026-09-04T00:00:00Z', expires_at: '2026-10-01T00:00:00Z' })
const noGrants = { 'GET /v1/account/oauth-grants': { body: { grants: [], truncated: false } } }

const confirmDialog = async (user: ReturnType<typeof userEvent.setup>) => {
  const dialog = await screen.findByRole('dialog')
  await user.click(within(dialog).getByRole('button', { name: 'Confirm' }))
}

describe('account tokens', () => {
  it('lists tokens, creates one with expiry and ceiling, reveals it once and revokes it', async () => {
    const user = userEvent.setup()
    let tokens = [visible, broker]
    const created = { id: 4, prefix: 'gnp_new', repository_ids: [101], created_at: '2026-01-01T00:00:00Z', expires_at: '2026-08-29T00:00:00Z', token: 'gnp_reveal_once' }
    const { calls } = mountConsole('/account', {
      ...noGrants,
      'GET /v1/account/api-tokens': () => ({ body: { tokens } }),
      'POST /v1/account/api-tokens': () => {
        tokens = [...tokens, created]
        return { status: 201, body: created }
      },
      'DELETE /v1/account/api-tokens/4': () => {
        tokens = tokens.filter((token) => token.id !== 4)
        return { status: 204 }
      },
    })
    const row = await screen.findByRole('row', { name: /gnp_visible/ })
    expect(within(row).getByText('101')).toBeInTheDocument()
    expect(within(row).getByText(date('2026-08-29T00:00:00Z'))).toBeInTheDocument()
    expect(within(row).getByText('—')).toBeInTheDocument()
    expect(within(screen.getByRole('row', { name: /gnp_broker__/ })).getByText('Delegation only (any active repository)')).toBeInTheDocument()

    const expires = screen.getByLabelText('Expires (optional, maximum 90 days)')
    expect(expires).toHaveAttribute('max')
    fireEvent.change(expires, { target: { value: '2026-08-29T00:00' } })
    await user.type(screen.getByLabelText('Repository IDs (required for administrator tokens)'), '101')
    await user.click(screen.getByRole('button', { name: 'Create API token' }))
    expect(await screen.findByText('Create this API token?')).toBeInTheDocument()
    await confirmDialog(user)

    expect(await screen.findByText('Copy this token now; it will not be shown again: gnp_reveal_once')).toBeInTheDocument()
    expect(calls.find((call) => call.method === 'POST')?.body).toEqual({ expires_at: new Date('2026-08-29T00:00').toISOString().replace('.000', ''), repository_ids: [101] })
    expect(await screen.findByRole('row', { name: /gnp_new/ })).toBeInTheDocument()

    await user.click(within(screen.getByRole('row', { name: /gnp_new/ })).getByRole('button', { name: 'Revoke token' }))
    expect(await screen.findByText('Revoke token gnp_new?')).toBeInTheDocument()
    await confirmDialog(user)
    expect(await screen.findByText('Token revoked.')).toBeInTheDocument()
    expect(calls.some((call) => call.method === 'DELETE' && call.path === '/v1/account/api-tokens/4')).toBe(true)
    // The secret is gone with the token it belonged to and was never fetched again.
    expect(screen.queryByText(/gnp_reveal_once/)).not.toBeInTheDocument()
    expect(screen.queryByRole('row', { name: /gnp_new/ })).not.toBeInTheDocument()
  })

  it('keeps the list empty and the form hidden when the credential has no account access', async () => {
    mountConsole('/account', { ...noGrants, 'GET /v1/account/api-tokens': failure(403) }, 'bearer')
    expect(await screen.findByText('No API tokens are active.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Create API token' })).not.toBeInTheDocument()
    expect(screen.getByText('API token management is unavailable for this credential.')).toBeInTheDocument()
    expect(sessionStorage.getItem('graphnest_token')).toBe('admin')
  })

  it('offers delegation tokens to session administrators only and reveals the secret once', async () => {
    const user = userEvent.setup()
    const { calls } = mountConsole('/account', {
      ...noGrants,
      'GET /v1/account/api-tokens': { body: { tokens: [] } },
      'POST /v1/account/delegation-tokens': { status: 201, body: { id: 6, prefix: 'gnp_deleg', delegation_only: true, created_at: '2026-01-01T00:00:00Z', token: 'gnp_delegation_secret' } },
    })
    await user.click(await screen.findByRole('button', { name: 'Create delegation token' }))
    await confirmDialog(user)
    expect(await screen.findByText('Copy this token now; it will not be shown again: gnp_delegation_secret')).toBeInTheDocument()
    expect(calls.find((call) => call.path === '/v1/account/delegation-tokens')?.body).toEqual({})
  })

  it.each([
    ['bearer administrator', 'bearer' as const, {}],
    ['session non-administrator', 'oidc' as const, { 'GET /v1/admin/overview': failure(403) }],
  ])('hides delegation tokens for a %s', async (_name, method, overrides) => {
    mountConsole('/account', { ...noGrants, 'GET /v1/account/api-tokens': { body: { tokens: [] } }, ...overrides }, method)
    expect(await screen.findByRole('button', { name: 'Create API token' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Create delegation token' })).not.toBeInTheDocument()
  })
})

describe('connected MCP clients', () => {
  it('follows the cursor to list every client and revokes the newest one', async () => {
    const user = userEvent.setup()
    const first = Array.from({ length: 100 }, (_, index) => grant(index + 9, index === 0 ? 'OpenCode' : `MCP client ${index + 1}`))
    const { calls } = mountConsole('/account', {
      'GET /v1/account/api-tokens': { body: { tokens: [] } },
      'GET /v1/account/oauth-grants': { body: { grants: first, truncated: true, next_cursor: 'grants-page-2' } },
      'GET /v1/account/oauth-grants?cursor=grants-page-2': { body: { grants: [grant(109, 'MCP client 101')], truncated: false } },
      'DELETE /v1/account/oauth-grants/109': { status: 204 },
    })
    const newest = await screen.findByRole('row', { name: /MCP client 101/ })
    expect(calls.some((call) => call.path === '/v1/account/oauth-grants?cursor=grants-page-2')).toBe(true)
    expect(screen.getByRole('row', { name: /OpenCode/ })).toBeInTheDocument()
    expect(within(newest).getByText(date('2026-09-04T00:00:00Z'))).toBeInTheDocument()
    // Client table: header plus 101 clients; the token table adds its own header and empty row.
    expect(screen.getAllByRole('row')).toHaveLength(1 + 1 + 1 + 101)

    await user.click(within(newest).getByRole('button', { name: 'Revoke access' }))
    expect(await screen.findByText('Disconnect MCP client 101? Its tokens stop working immediately.')).toBeInTheDocument()
    await confirmDialog(user)
    expect(await screen.findByText('Client disconnected.')).toBeInTheDocument()
    expect(calls.some((call) => call.method === 'DELETE' && call.path === '/v1/account/oauth-grants/109')).toBe(true)
  })

  it('reports a failed page and a truncated page without a cursor', async () => {
    mountConsole('/account', {
      'GET /v1/account/api-tokens': { body: { tokens: [] } },
      'GET /v1/account/oauth-grants': { body: { grants: [grant(9, 'OpenCode')], truncated: true, next_cursor: 'p2' } },
      'GET /v1/account/oauth-grants?cursor=p2': failure(503, 'Grant pagination failed.'),
    })
    expect(await screen.findByText('Grant pagination failed.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create API token' })).toBeInTheDocument()
  })

  it('treats a truncated page without a cursor as incomplete', async () => {
    mountConsole('/account', {
      'GET /v1/account/api-tokens': { body: { tokens: [] } },
      'GET /v1/account/oauth-grants': { body: { grants: [grant(9, 'OpenCode')], truncated: true } },
    })
    expect(await screen.findByText('Connected-client list is incomplete.')).toBeInTheDocument()
  })

  it('shows the empty state', async () => {
    mountConsole('/account', { 'GET /v1/account/api-tokens': { body: { tokens: [] } }, ...noGrants })
    expect(await screen.findByText('No MCP clients are connected.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Connected MCP clients' })).toBeInTheDocument()
  })
})
