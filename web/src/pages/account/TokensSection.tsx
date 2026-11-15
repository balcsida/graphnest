import { useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { createApiToken, createDelegationToken, createInventoryToken, getApiTokens, revokeApiToken } from '@/api/account'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ApiError } from '@/lib/api'
import { bounded, parseIds, when } from '@/pages/admin/format'
import { EmptyRow, ErrorNotice, Panel, mono } from '@/pages/admin/shared'
import { useAdminActions } from '@/pages/admin/use-admin-actions'
import { TokenReveal } from './TokenReveal'

const DAY = 24 * 60 * 60 * 1000

/** datetime-local value (local time, minute precision) for the latest allowed expiry. */
function expiryMax(): string {
  const limit = new Date(Date.now() + 90 * DAY)
  limit.setMinutes(limit.getMinutes() - limit.getTimezoneOffset())
  return limit.toISOString().slice(0, 16)
}

const expiryBody = (value: string) => (value ? { expires_at: new Date(value).toISOString().replace('.000', '') } : {})

type ServiceKind = 'delegation' | 'inventory'

/** The single-purpose administrator token kinds: no repository ceiling, one permitted use each. */
const serviceTokens: Record<ServiceKind, { create: (body: { expires_at?: string }) => Promise<{ id: number; token: string }>; description: string }> = {
  delegation: {
    create: createDelegationToken,
    description:
      'A delegation-only administrator token has no repository access of its own. Its only use is minting short-lived, single-repository tokens for CI jobs through POST /v1/admin/api-tokens.',
  },
  inventory: {
    create: createInventoryToken,
    description:
      'An inventory-only administrator token lists every active repository and its index status through GET /v1/repositories. It cannot search, read files, upload or mint tokens.',
  },
}

const capitalized = (kind: ServiceKind) => kind[0].toUpperCase() + kind.slice(1)

interface Revealed {
  id: number
  token: string
  form: 'api' | ServiceKind
}

function ServiceTokenPanel({ kind, max, revealed, onCreate }: { kind: ServiceKind; max: string; revealed: string; onCreate: (body: { expires_at?: string }) => Promise<unknown> }) {
  const [expires, setExpires] = useState('')
  return (
    <Panel title={`Create ${kind} token`}>
      <p className="text-muted-foreground">{serviceTokens[kind].description}</p>
      <form
        className="grid gap-3"
        onSubmit={(event) => {
          event.preventDefault()
          void onCreate(expiryBody(expires))
        }}
      >
        <div className="grid gap-1.5">
          <Label htmlFor={`${kind}-expires`}>{`${capitalized(kind)} token expires (optional, maximum 90 days)`}</Label>
          <Input id={`${kind}-expires`} type="datetime-local" max={max} value={expires} onChange={(event) => setExpires(event.target.value)} />
        </div>
        <Button type="submit" variant="outline">
          {`Create ${kind} token`}
        </Button>
        <TokenReveal token={revealed} />
      </form>
    </Panel>
  )
}

/** `administrator`: an interactive administrator, the only caller offered the single-purpose kinds. */
export function TokensSection({ administrator }: { administrator: boolean }) {
  const query = useQuery({ queryKey: ['account', 'api-tokens'], queryFn: ({ signal }) => getApiTokens(signal), refetchInterval: 30_000, retry: false })
  const { confirmed, dialog } = useAdminActions()
  const [max] = useState(expiryMax)
  const [expires, setExpires] = useState('')
  const [repositories, setRepositories] = useState('')
  const [error, setError] = useState('')
  const [revealed, setRevealed] = useState<Revealed | null>(null)

  // Credentials without account access (for example a static administrator token) get an empty, read-only view.
  const unavailable = query.error instanceof ApiError && query.error.status === 403
  const tokens = bounded(query.data?.tokens)

  async function create(event: FormEvent) {
    event.preventDefault()
    const ids = parseIds(repositories)
    if (ids === null) {
      setError('Repository IDs must be positive whole numbers.')
      return
    }
    setError('')
    await confirmed(
      'Create this API token?',
      () => createApiToken({ ...expiryBody(expires), repository_ids: ids }),
      'API token created.',
      'account',
      (created) => setRevealed({ id: created.id, token: created.token, form: 'api' }),
    )
  }

  const createService = (kind: ServiceKind) => (body: { expires_at?: string }) =>
    confirmed(
      `Create this ${kind}-only token?`,
      () => serviceTokens[kind].create(body),
      `${capitalized(kind)} token created.`,
      'account',
      (created) => setRevealed({ id: created.id, token: created.token, form: kind }),
    )

  async function revoke(id: number, prefix: string) {
    if (await confirmed(`Revoke token ${prefix}?`, () => revokeApiToken(id), 'Token revoked.', 'account')) {
      setRevealed((current) => (current?.id === id ? null : current))
    }
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1.5fr)_minmax(280px,1fr)]">
      <div className="grid content-start gap-4">
        {query.isError && !unavailable && <ErrorNotice error={query.error} onRetry={() => void query.refetch()} />}
        {query.isPending ? (
          <Skeleton className="h-32 w-full" aria-label="Loading API tokens" />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead scope="col">Token</TableHead>
                <TableHead scope="col">Repositories</TableHead>
                <TableHead scope="col">Created</TableHead>
                <TableHead scope="col">Last used</TableHead>
                <TableHead scope="col">Expires</TableHead>
                <TableHead scope="col">Action</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tokens.length === 0 && <EmptyRow columns={6}>No API tokens are active.</EmptyRow>}
              {tokens.map((token) => (
                <TableRow key={token.id}>
                  <TableCell className={mono}>{token.prefix}</TableCell>
                  <TableCell className="whitespace-normal">
                    {token.delegation_only
                      ? 'Delegation only (any active repository)'
                      : token.inventory_only
                        ? 'Inventory only (lists every active repository)'
                        : (token.repository_ids ?? []).join(', ')}
                  </TableCell>
                  <TableCell>{when(token.created_at)}</TableCell>
                  <TableCell>{when(token.last_used_at)}</TableCell>
                  <TableCell>{when(token.expires_at)}</TableCell>
                  <TableCell>
                    <Button type="button" size="sm" variant="outline" onClick={() => void revoke(token.id, token.prefix)}>
                      Revoke token
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
      <div className="grid content-start gap-4">
        {unavailable ? (
          <p className="text-sm text-muted-foreground">API token management is unavailable for this credential.</p>
        ) : (
          <Panel title="Create API token">
            <form className="grid gap-3" onSubmit={(event) => void create(event)}>
              <div className="grid gap-1.5">
                <Label htmlFor="token-expires">Expires (optional, maximum 90 days)</Label>
                <Input id="token-expires" type="datetime-local" max={max} value={expires} onChange={(event) => setExpires(event.target.value)} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="token-repositories">Repository IDs (required for administrator tokens)</Label>
                <Input id="token-repositories" inputMode="numeric" required value={repositories} onChange={(event) => setRepositories(event.target.value)} />
              </div>
              {error && (
                <p role="alert" className="text-sm text-destructive">
                  {error}
                </p>
              )}
              <Button type="submit">Create API token</Button>
              <TokenReveal token={revealed?.form === 'api' ? revealed.token : ''} />
            </form>
          </Panel>
        )}
        {administrator &&
          !unavailable &&
          (Object.keys(serviceTokens) as ServiceKind[]).map((kind) => (
            <ServiceTokenPanel key={kind} kind={kind} max={max} revealed={revealed?.form === kind ? revealed.token : ''} onCreate={createService(kind)} />
          ))}
      </div>
      {dialog}
    </div>
  )
}
