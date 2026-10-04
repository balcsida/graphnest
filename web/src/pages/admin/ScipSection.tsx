import { useRef, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'
import { getScipDependencies, getScipUploads, refreshGitHubDependencies, uploadScipIndex } from '@/api/admin'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { bounded, parseId, short } from './format'
import { Panel, PartialNotice, QueryBoundary, mono } from './shared'
import { useAdminActions } from './use-admin-actions'

export function ScipSection() {
  const uploads = useQuery({ queryKey: ['admin', 'scip', 'uploads'], queryFn: ({ signal }) => getScipUploads(signal), refetchInterval: 30_000, retry: false })
  const dependencies = useQuery({ queryKey: ['admin', 'scip', 'dependencies'], queryFn: ({ signal }) => getScipDependencies(signal), refetchInterval: 30_000, retry: false })
  const { confirmed, dialog } = useAdminActions()
  const [repository, setRepository] = useState('')
  const [commit, setCommit] = useState('')
  const [dependencyRepository, setDependencyRepository] = useState('')
  const file = useRef<HTMLInputElement>(null)

  const partial = [uploads.data?.truncated && 'SCIP indexes', dependencies.data?.truncated && 'SCIP dependencies'].filter((name): name is string => Boolean(name))

  async function upload(event: FormEvent) {
    event.preventDefault()
    const chosen = file.current?.files?.[0]
    const repositoryId = parseId(repository)
    if (!chosen) return
    if (repositoryId === null) {
      toast.error('Repository ID must be a positive whole number.')
      return
    }
    await confirmed(`Upload ${chosen.name} for repository ${repository}?`, () => uploadScipIndex(repositoryId, commit, chosen), 'SCIP index uploaded.')
  }

  async function refresh(event: FormEvent) {
    event.preventDefault()
    const repositoryId = parseId(dependencyRepository)
    if (repositoryId === null) {
      toast.error('Repository ID must be a positive whole number.')
      return
    }
    await confirmed('Replace dependency metadata with GitHub dependency graph data?', () => refreshGitHubDependencies(repositoryId), 'GitHub dependencies refreshed.')
  }

  return (
    <div className="grid gap-4">
      {partial.length > 0 && <PartialNotice names={partial} />}
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1.5fr)_minmax(280px,1fr)]">
        <div className="grid content-start gap-4">
          <Panel title="SCIP indexes">
            <QueryBoundary query={uploads} label="SCIP indexes">
              {(data) => {
                const rows = bounded(data.uploads)
                return rows.length === 0 ? (
                  <p className="text-muted-foreground">No SCIP indexes uploaded.</p>
                ) : (
                  <ul className="grid gap-2">
                    {rows.map((row) => (
                      <li key={row.id} className={mono}>
                        {row.repository} · {short(row.commit)} · {row.indexer_name} {row.indexer_version}
                      </li>
                    ))}
                  </ul>
                )
              }}
            </QueryBoundary>
          </Panel>
          <Panel title="Package dependencies">
            <QueryBoundary query={dependencies} label="package dependencies">
              {(data) => {
                const rows = bounded(data.dependencies)
                return rows.length === 0 ? (
                  <p className="text-muted-foreground">No package dependency metadata available.</p>
                ) : (
                  <ul className="grid gap-2">
                    {rows.map((row) => (
                      <li key={`${row.repository_id}-${row.source}-${row.relation}-${row.purl}`} className={mono}>
                        {row.repository} · {row.relation} · {row.purl} · {row.source}
                      </li>
                    ))}
                  </ul>
                )
              }}
            </QueryBoundary>
          </Panel>
        </div>
        <div className="grid content-start gap-4">
          <Panel title="Upload SCIP index">
            <form className="grid gap-3" onSubmit={(event) => void upload(event)}>
              <div className="grid gap-1.5">
                <Label htmlFor="scip-repo">Repository ID</Label>
                <Input id="scip-repo" inputMode="numeric" required value={repository} onChange={(event) => setRepository(event.target.value)} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="scip-commit">Indexed commit</Label>
                <Input id="scip-commit" pattern="[0-9a-fA-F]{40}" required value={commit} onChange={(event) => setCommit(event.target.value)} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="scip-file">SCIP file</Label>
                <Input id="scip-file" type="file" accept=".scip,application/vnd.scip+protobuf" required ref={file} />
              </div>
              <Button type="submit">Upload</Button>
            </form>
          </Panel>
          <Panel title="Refresh from GitHub">
            <form className="grid gap-3" onSubmit={(event) => void refresh(event)}>
              <div className="grid gap-1.5">
                <Label htmlFor="dependency-repo">Repository ID</Label>
                <Input id="dependency-repo" inputMode="numeric" required value={dependencyRepository} onChange={(event) => setDependencyRepository(event.target.value)} />
              </div>
              <Button type="submit" variant="outline">
                Refresh dependencies
              </Button>
            </form>
          </Panel>
        </div>
      </div>
      {dialog}
    </div>
  )
}
