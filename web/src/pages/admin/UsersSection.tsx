import { useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getAdminUsers, replaceUserAccess, restoreUser, revokeUserCredentials, suspendUser } from '@/api/admin'
import type { AdminUser } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { emptyDraft, type AccessDraft } from './access-draft'
import { AccessForm } from './AccessForm'
import { accessLabel, bounded, directAccessLabel, parseId, parseIds } from './format'
import { EmptyRow, PartialNotice, QueryBoundary, StatusPill, mono } from './shared'
import { useAdminActions } from './use-admin-actions'

export function UsersSection() {
  const query = useQuery({ queryKey: ['admin', 'users'], queryFn: ({ signal }) => getAdminUsers(signal), refetchInterval: 30_000, retry: false })
  const { confirmed, dialog } = useAdminActions()
  const [draft, setDraft] = useState<AccessDraft>(emptyDraft)
  const [error, setError] = useState('')
  const idRef = useRef<HTMLInputElement>(null)

  function edit(user: AdminUser) {
    setDraft({ id: String(user.id), administrator: user.direct_administrator, repositories: (user.direct_repository_ids ?? []).join(',') })
    setError('')
    idRef.current?.focus()
  }

  async function save() {
    const id = parseId(draft.id)
    const ids = parseIds(draft.repositories)
    if (id === null || ids === null) {
      setError('User ID and repository IDs must be positive whole numbers.')
      return
    }
    setError('')
    await confirmed('Replace direct access for this user?', () => replaceUserAccess(id, { direct_administrator: draft.administrator, direct_repository_ids: ids }), 'Access replaced.')
  }

  return (
    <QueryBoundary query={query} label="users">
      {(data) => {
        const users = bounded(data.users)
        return (
          <div className="grid gap-4">
            {data.truncated && <PartialNotice names={['users']} />}
            <div className="grid gap-4 lg:grid-cols-[minmax(0,1.5fr)_minmax(280px,1fr)]">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead scope="col">User</TableHead>
                    <TableHead scope="col">State</TableHead>
                    <TableHead scope="col">Effective access</TableHead>
                    <TableHead scope="col">Direct access</TableHead>
                    <TableHead scope="col">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {users.length === 0 && <EmptyRow columns={5}>No users are available.</EmptyRow>}
                  {users.map((user) => {
                    const name = user.user_name || user.display_name
                    return (
                      <TableRow key={user.id}>
                        <TableCell className={mono}>{name}</TableCell>
                        <TableCell>
                          <StatusPill value={user.suspended ? 'suspended' : user.scim_active ? 'active' : 'inactive'} />
                        </TableCell>
                        <TableCell className="whitespace-normal">{accessLabel(user.administrator, user.repository_ids)}</TableCell>
                        <TableCell className="whitespace-normal">{directAccessLabel(user.direct_administrator, user.direct_repository_ids, user.github_repository_ids)}</TableCell>
                        <TableCell>
                          <div className="flex flex-wrap gap-2">
                            <Button type="button" size="sm" variant="outline" onClick={() => edit(user)}>
                              Edit access
                            </Button>
                            <Button
                              type="button"
                              size="sm"
                              variant="outline"
                              onClick={() =>
                                void (user.suspended
                                  ? confirmed(`Restore ${name}?`, () => restoreUser(user.id), 'User updated.')
                                  : confirmed(`Suspend ${name}?`, () => suspendUser(user.id), 'User updated.'))
                              }
                            >
                              {user.suspended ? 'Restore user' : 'Suspend user'}
                            </Button>
                            <Button type="button" size="sm" variant="outline" onClick={() => void confirmed(`Revoke all credentials for ${name}?`, () => revokeUserCredentials(user.id), 'Credentials revoked.')}>
                              Revoke credentials
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
              <AccessForm kind="user" title="Replace direct access" idLabel="User ID" submitLabel="Save direct access" draft={draft} idRef={idRef} error={error} onChange={setDraft} onSubmit={() => void save()} />
            </div>
            {dialog}
          </div>
        )
      }}
    </QueryBoundary>
  )
}
