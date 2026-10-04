import { Link } from 'react-router'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Pill } from '../pill'
import { licenseTone } from '../format'
import { KeyValues } from '../shared'
import type { DependencyGraph, GraphDependency, GraphRepository } from './build-graph'

export type Selection = { kind: 'repository'; id: number } | { kind: 'dependency'; id: string }

const inventoryLink = (stream: string, repositoryId: number) => `/supply-chain/repositories?${new URLSearchParams({ repo: String(repositoryId), stream }).toString()}`

const componentsLink = (stream: string, dependency: GraphDependency) =>
  `/supply-chain/components?${new URLSearchParams({ stream, ecosystem: dependency.ecosystem, q: dependency.name }).toString()}`

const linkClass = 'underline underline-offset-2'

function DependencyDetails({ dependency, graph, stream }: { dependency: GraphDependency; graph: DependencyGraph; stream: string }) {
  const names = new Map(graph.repositories.map((repository) => [repository.id, repository.name]))
  return (
    <>
      <KeyValues
        rows={[
          ['Ecosystem', dependency.ecosystem],
          ['Versions', dependency.versions.join(', ') || '—'],
          ['License expressions', dependency.expressions.join(', ') || '—'],
        ]}
      />
      <div className="grid gap-1">
        <h3 className="text-sm font-medium">Assessment statuses</h3>
        <p className="flex flex-wrap gap-1.5">
          {dependency.assessments.map((status) => (
            <Pill key={status} value={status} tone={licenseTone(status)} label="Assessment" />
          ))}
        </p>
      </div>
      <div className="grid gap-1">
        <h3 className="text-sm font-medium">Repositories using it ({dependency.repositoryIds.length})</h3>
        <ul className="grid gap-1 text-sm">
          {dependency.repositoryIds.map((id) => (
            <li key={id}>
              <Link className={linkClass} to={inventoryLink(stream, id)}>
                {names.get(id) ?? `Repository ${id}`}
              </Link>
            </li>
          ))}
        </ul>
      </div>
      <Link className={linkClass} to={componentsLink(stream, dependency)}>
        Open in the components list
      </Link>
    </>
  )
}

function RepositoryDetails({ repository, graph, stream }: { repository: GraphRepository; graph: DependencyGraph; stream: string }) {
  const dependencies = graph.dependencies.filter((dependency) => repository.dependencyIds.includes(dependency.id))
  return (
    <>
      <div className="grid gap-1">
        <h3 className="text-sm font-medium">Dependencies in the graph ({dependencies.length})</h3>
        <ul className="grid gap-1 text-sm">
          {dependencies.map((dependency) => (
            <li key={dependency.id}>
              <span className="break-all">{dependency.label}</span> <span className="text-muted-foreground">({dependency.ecosystem}, {dependency.versions.join(', ') || 'no version'})</span>
            </li>
          ))}
        </ul>
      </div>
      <Link className={linkClass} to={inventoryLink(stream, repository.id)}>
        Open the repository inventory
      </Link>
    </>
  )
}

/** Side sheet for a clicked node; Escape and the close button call `onClose`. */
export function GraphSheet({ selection, graph, stream, onClose }: { selection: Selection | undefined; graph: DependencyGraph; stream: string; onClose: () => void }) {
  const dependency = selection?.kind === 'dependency' ? graph.dependencies.find((item) => item.id === selection.id) : undefined
  const repository = selection?.kind === 'repository' ? graph.repositories.find((item) => item.id === selection.id) : undefined
  const title = dependency?.label ?? repository?.name ?? ''
  return (
    <Sheet open={Boolean(dependency ?? repository)} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="overflow-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle className="break-all">{title}</SheetTitle>
          <SheetDescription>{dependency ? 'Dependency used by the repositories listed below.' : 'Repository and the dependencies it contributes to this graph.'}</SheetDescription>
        </SheetHeader>
        <div className="grid gap-4 px-4 pb-4 text-sm">
          {dependency && <DependencyDetails dependency={dependency} graph={graph} stream={stream} />}
          {repository && <RepositoryDetails repository={repository} graph={graph} stream={stream} />}
        </div>
      </SheetContent>
    </Sheet>
  )
}
