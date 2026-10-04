import { useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getAdminGroups, replaceGroupAccess } from '@/api/admin'
import type { AdminGroup } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { emptyDraft, type AccessDraft } from './access-draft'
import { AccessForm } from './AccessForm'
import { accessLabel, bounded, parseId, parseIds } from './format'
import { EmptyRow, PartialNotice, QueryBoundary, mono } from './shared'
import { useAdminActions } from './use-admin-actions'

export function GroupsSection() {
  const query = useQuery({ queryKey: ['admin', 'groups'], queryFn: ({ signal }) => getAdminGroups(signal), refetchInterval: 30_000, retry: false })
  const { confirmed, dialog } = useAdminActions()
  const [draft, setDraft] = useState<AccessDraft>(emptyDraft)
  const [error, setError] = useState('')
  const idRef = useRef<HTMLInputElement>(null)

  function edit(group: AdminGroup) {
    setDraft({ id: String(group.id), administrator: group.administrator, repositories: (group.repository_ids ?? []).join(',') })
    setError('')
    idRef.current?.focus()
  }

  async function save() {
    const id = parseId(draft.id)
    const ids = parseIds(draft.repositories)
    if (id === null || ids === null) {
      setError('Group ID and repository IDs must be positive whole numbers.')
      return
    }
    setError('')
    await confirmed('Replace direct access for this group?', () => replaceGroupAccess(id, { administrator: draft.administrator, repository_ids: ids }), 'Access replaced.')
  }

  return (
    <QueryBoundary query={query} label="groups">
      {(data) => {
        const groups = bounded(data.groups)
        return (
          <div className="grid gap-4">
            {data.truncated && <PartialNotice names={['groups']} />}
            <div className="grid gap-4 lg:grid-cols-[minmax(0,1.5fr)_minmax(280px,1fr)]">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead scope="col">Group</TableHead>
                    <TableHead scope="col">Members</TableHead>
                    <TableHead scope="col">Effective access</TableHead>
                    <TableHead scope="col">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {groups.length === 0 && <EmptyRow columns={4}>No groups are available.</EmptyRow>}
                  {groups.map((group) => (
                    <TableRow key={group.id}>
                      <TableCell className={mono}>{group.display_name}</TableCell>
                      <TableCell>{String(group.member_count || 0)}</TableCell>
                      <TableCell className="whitespace-normal">{accessLabel(group.administrator, group.repository_ids)}</TableCell>
                      <TableCell>
                        <Button type="button" size="sm" variant="outline" onClick={() => edit(group)}>
                          Edit access
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              <AccessForm kind="group" title="Replace group access" idLabel="Group ID" submitLabel="Save group access" draft={draft} idRef={idRef} error={error} onChange={setDraft} onSubmit={() => void save()} />
            </div>
            {dialog}
          </div>
        )
      }}
    </QueryBoundary>
  )
}
