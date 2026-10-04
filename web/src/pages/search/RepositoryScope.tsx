import { ChevronDown } from 'lucide-react'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Label } from '@/components/ui/label'
import { countLabel } from './model'
import { useSearchState } from './state'
import { useRepositories } from './use-repositories'
import { useScope } from './use-scope'

export function RepositoryScope() {
  const { allRepositories, setAllRepositories, selected, toggleRepository } = useSearchState()
  const { repositories, hasMore, status, isError, isLoading, loadMore } = useRepositories()
  const scope = useScope()

  return (
    <Collapsible className="grid gap-2">
      <CollapsibleTrigger asChild>
        <Button type="button" variant="outline" className="justify-between">
          <span>{scope ? countLabel(scope.length, 'repository') : 'All repositories'}</span>
          <ChevronDown aria-hidden="true" />
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <fieldset className="grid gap-2 rounded-md border p-3">
          <legend className="sr-only">Search repositories</legend>
          <div className="flex items-center gap-2">
            <Checkbox id="all-repositories" checked={allRepositories} onCheckedChange={(checked) => setAllRepositories(checked === true)} />
            <Label htmlFor="all-repositories">All authorized repositories</Label>
          </div>
          {repositories.map((repository) => (
            <div key={repository.name} className="flex min-w-0 items-center gap-2">
              <Checkbox
                id={`repository-${repository.id}`}
                disabled={allRepositories}
                checked={selected.has(repository.name)}
                onCheckedChange={(checked) => toggleRepository(repository.name, checked === true)}
              />
              <Label htmlFor={`repository-${repository.id}`} className="min-w-0 break-all">
                {repository.name}
              </Label>
            </div>
          ))}
          {hasMore && (
            <Button type="button" variant="secondary" size="sm" disabled={isLoading} onClick={loadMore}>
              Load more repositories
            </Button>
          )}
          {isError ? (
            <Alert variant="destructive">
              <AlertDescription>{status}</AlertDescription>
            </Alert>
          ) : (
            <p role="status" aria-live="polite" className="text-xs text-muted-foreground">
              {status}
            </p>
          )}
        </fieldset>
      </CollapsibleContent>
    </Collapsible>
  )
}
