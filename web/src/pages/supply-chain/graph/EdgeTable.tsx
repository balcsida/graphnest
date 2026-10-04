import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { DependencyGraph } from './build-graph'
import type { Selection } from './GraphSheet'

/** The graph's edges as a table: the keyboard and screen-reader alternative to the canvas. */
export function EdgeTable({ graph, onSelect }: { graph: DependencyGraph; onSelect: (selection: Selection) => void }) {
  const repositories = new Map(graph.repositories.map((repository) => [repository.id, repository]))
  const dependencies = new Map(graph.dependencies.map((dependency) => [dependency.id, dependency]))
  return (
    <Table>
      <caption className="sr-only">Repositories and the shared dependencies they use</caption>
      <TableHeader>
        <TableRow>
          <TableHead>Repository</TableHead>
          <TableHead>Dependency</TableHead>
          <TableHead>Ecosystem</TableHead>
          <TableHead>Versions</TableHead>
          <TableHead>Assessments</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {graph.edges.map((edge) => {
          const repository = repositories.get(edge.repositoryId)
          const dependency = dependencies.get(edge.dependencyId)
          if (!repository || !dependency) return null
          return (
            <TableRow key={`${edge.repositoryId}->${edge.dependencyId}`}>
              <TableCell>
                <Button type="button" variant="link" className="h-auto p-0 break-all whitespace-normal" onClick={() => onSelect({ kind: 'repository', id: repository.id })}>
                  {repository.name}
                </Button>
              </TableCell>
              <TableCell>
                <Button type="button" variant="link" className="h-auto p-0 break-all whitespace-normal" onClick={() => onSelect({ kind: 'dependency', id: dependency.id })}>
                  {dependency.label}
                </Button>
              </TableCell>
              <TableCell>{dependency.ecosystem}</TableCell>
              <TableCell className="break-all whitespace-normal">{dependency.versions.join(', ') || '—'}</TableCell>
              <TableCell>{dependency.assessments.join(', ') || '—'}</TableCell>
            </TableRow>
          )
        })}
        {graph.edges.length === 0 && (
          <TableRow>
            <TableCell colSpan={5} className="py-6 text-center text-muted-foreground">
              No shared dependencies match these filters
            </TableCell>
          </TableRow>
        )}
      </TableBody>
    </Table>
  )
}
