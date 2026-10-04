import { useQuery } from '@tanstack/react-query'
import { getOAuthGrants, revokeOAuthGrant } from '@/api/account'
import type { OAuthGrant } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { when } from '@/pages/admin/format'
import { UserFacingError } from '@/pages/search/model'
import { EmptyRow, ErrorNotice, mono } from '@/pages/admin/shared'
import { useAdminActions } from '@/pages/admin/use-admin-actions'

/** Follows the cursor until the server stops truncating; a truncated page without a cursor is an error. */
async function loadAllGrants(signal: AbortSignal): Promise<OAuthGrant[]> {
  const grants: OAuthGrant[] = []
  const seen = new Set<string>()
  let cursor: string | undefined
  do {
    const page = await getOAuthGrants(cursor, signal)
    grants.push(...(Array.isArray(page.grants) ? page.grants : []))
    if (page.truncated && (!page.next_cursor || seen.has(page.next_cursor))) throw new UserFacingError('Connected-client list is incomplete.')
    cursor = page.truncated ? page.next_cursor : undefined
    if (cursor) seen.add(cursor)
  } while (cursor)
  return grants
}

export function GrantsSection() {
  const query = useQuery({ queryKey: ['account', 'oauth-grants'], queryFn: ({ signal }) => loadAllGrants(signal), refetchInterval: 30_000, retry: false })
  const { confirmed, dialog } = useAdminActions()
  return (
    <section className="grid gap-3" aria-labelledby="mcp-clients">
      <h2 id="mcp-clients" className="text-sm font-semibold text-muted-foreground uppercase">
        Connected MCP clients
      </h2>
      {query.isError && <ErrorNotice error={query.error} onRetry={() => void query.refetch()} />}
      {query.isPending ? (
        <Skeleton className="h-24 w-full" aria-label="Loading connected clients" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead scope="col">Client</TableHead>
              <TableHead scope="col">Authorized</TableHead>
              <TableHead scope="col">Last used</TableHead>
              <TableHead scope="col">Expires</TableHead>
              <TableHead scope="col">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(query.data ?? []).length === 0 && <EmptyRow columns={5}>No MCP clients are connected.</EmptyRow>}
            {(query.data ?? []).map((grant) => (
              <TableRow key={grant.id}>
                <TableCell className={mono}>{grant.client_name}</TableCell>
                <TableCell>{when(grant.created_at)}</TableCell>
                <TableCell>{when(grant.last_used_at)}</TableCell>
                <TableCell>{when(grant.expires_at)}</TableCell>
                <TableCell>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => void confirmed(`Disconnect ${grant.client_name}? Its tokens stop working immediately.`, () => revokeOAuthGrant(grant.id), 'Client disconnected.', 'account')}
                  >
                    Revoke access
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {dialog}
    </section>
  )
}
