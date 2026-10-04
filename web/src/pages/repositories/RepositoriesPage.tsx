import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatIndexedAt, repositoryUrl, shortSha } from '@/pages/search/model'
import { useRepositories } from '@/pages/search/use-repositories'

const statusStyles: Record<string, string> = {
  ready: 'text-emerald-700 dark:text-emerald-400',
  pending: 'text-amber-700 dark:text-amber-400',
  failed: 'text-destructive',
  disabled: 'text-muted-foreground',
}

// Below md each row becomes a card: the header is hidden and every cell is labelled by its data-label.
const cell = 'max-md:grid max-md:grid-cols-[7rem_1fr] max-md:gap-2 max-md:before:text-xs max-md:before:text-muted-foreground max-md:before:content-[attr(data-label)]'

export default function RepositoriesPage() {
  const { repositories, hasMore, status, isError, isLoading, loadMore } = useRepositories()
  const first = repositories.length === 0 && isLoading

  return (
    <div className="grid gap-4">
      <div className="grid gap-1">
        <h1 className="text-xl font-semibold">Repositories</h1>
        <p className="text-sm text-muted-foreground">Everything your token is authorized to search.</p>
      </div>
      {isError ? (
        <Alert variant="destructive">
          <AlertDescription>{status}</AlertDescription>
        </Alert>
      ) : (
        <p role="status" aria-live="polite" className="text-sm text-muted-foreground">
          {status}
        </p>
      )}
      {first ? (
        <Skeleton className="h-32 w-full" aria-label="Loading repositories" />
      ) : (
        <Table className="max-md:block">
          <TableHeader className="max-md:sr-only">
            <TableRow>
              <TableHead scope="col" className="w-1/3">
                Repository
              </TableHead>
              <TableHead scope="col">Branch</TableHead>
              <TableHead scope="col">Status</TableHead>
              <TableHead scope="col">Indexed SHA</TableHead>
              <TableHead scope="col">Last indexed</TableHead>
              <TableHead scope="col">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody className="max-md:block">
            {repositories.map((repository) => {
              const href = repositoryUrl(repository.web_url)
              return (
                <TableRow key={repository.name} className="max-md:mb-3 max-md:block max-md:rounded-md max-md:border">
                  <TableCell data-label="Repository" className={`${cell} font-mono break-all whitespace-normal`}>
                    {repository.name}
                  </TableCell>
                  <TableCell data-label="Branch" className={cell}>
                    {repository.branch || '—'}
                  </TableCell>
                  <TableCell data-label="Status" className={cell}>
                    <span className={`inline-flex items-center gap-1.5 ${statusStyles[repository.status] ?? statusStyles.pending}`}>
                      <span aria-hidden="true" className="size-1.5 rounded-full bg-current" />
                      {repository.status || 'unknown'}
                    </span>
                  </TableCell>
                  <TableCell data-label="Indexed SHA" className={`${cell} font-mono`}>
                    {repository.indexed_sha ? shortSha(repository.indexed_sha) : '—'}
                  </TableCell>
                  <TableCell data-label="Last indexed" className={cell}>
                    {formatIndexedAt(repository.last_indexed_at)}
                  </TableCell>
                  <TableCell data-label="Open" className={cell}>
                    {href ? (
                      <a href={href} target="_blank" rel="noopener noreferrer" className="underline underline-offset-4">
                        Open ↗
                      </a>
                    ) : (
                      '—'
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}
      {hasMore && (
        <Button type="button" variant="secondary" className="justify-self-start" disabled={isLoading} onClick={loadMore}>
          Load more repositories
        </Button>
      )}
    </div>
  )
}
