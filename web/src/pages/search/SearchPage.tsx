import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { CircleHelp, Search } from 'lucide-react'
import { searchCode } from '@/api/search'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/lib/auth'
import { ErrorAlert } from './ErrorAlert'
import { FileViewer } from './FileViewer'
import { RepositoryScope } from './RepositoryScope'
import { ResultsList } from './ResultsList'
import { SyntaxDrawer } from './SyntaxDrawer'
import { EmptyScopeError, countLabel, groupMatches, queryWithLanguage } from './model'
import { useSearchState } from './state'
import { useRepositories } from './use-repositories'
import { useScope } from './use-scope'

const examples = ['lang:go -test NewService', 'case:yes repo:payments Token']
const languages = ['go', 'javascript', 'python', 'typescript']
const anyLanguage = 'any'
const scopeError = new EmptyScopeError()

export default function SearchPage() {
  const { file_reads } = useAuth()
  const state = useSearchState()
  const { query, setQuery, language, setLanguage, submission, submit, file, openFile, closeFile, dropFile, fileTrigger, queryInput, focusTick, restoreTick } = state
  const scope = useScope()
  const repositories = useRepositories()
  const [syntaxOpen, setSyntaxOpen] = useState(false)
  const syntaxOpener = useRef<HTMLElement | null>(null)

  const scopeEmpty = submission?.repositories?.length === 0
  const search = useQuery({
    queryKey: ['search', submission?.id],
    queryFn: ({ signal }) => searchCode({ query: submission?.query ?? '', repositories: submission?.repositories }, signal),
    enabled: submission !== null && !scopeEmpty,
    retry: false,
    staleTime: Infinity,
    // Keep the previous results on screen while a new search runs; a cleared submission drops them.
    placeholderData: (previous) => (submission ? previous : undefined),
  })

  useEffect(() => queryInput.current?.focus(), [focusTick, queryInput])

  // Back from the file viewer returns focus to the path button that opened it, or to the query.
  useEffect(() => {
    if (restoreTick === 0) return
    const trigger = fileTrigger.current
    ;(trigger?.isConnected ? trigger : queryInput.current)?.focus()
  }, [restoreTick, fileTrigger, queryInput])

  useEffect(() => dropFile, [dropFile])

  const groups = useMemo(() => groupMatches(search.data?.matches ?? []), [search.data])

  function onSubmit(event: FormEvent) {
    event.preventDefault()
    const text = queryWithLanguage(query, language)
    if (text.trim()) submit(text, scope)
  }

  function onQueryKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') event.currentTarget.form?.requestSubmit()
  }

  function openSyntax(event: { currentTarget: HTMLElement }) {
    syntaxOpener.current = event.currentTarget
    setSyntaxOpen(true)
  }

  function applyExample(text: string) {
    setQuery(text)
    queryInput.current?.focus()
  }

  let status = ''
  if (search.isFetching) status = 'Searching indexed code…'
  else if (search.data) {
    status = `${countLabel(search.data.matches.length, 'match')} in ${countLabel(groups.length, 'repository')}${search.data.truncated ? '; results were truncated.' : '.'}`
  } else if (repositories.isError) status = 'Repository choices are unavailable; searching all authorized repositories.'

  const error = scopeEmpty ? scopeError : search.isError ? search.error : null

  return (
    <>
      <div hidden={file !== null} className="grid gap-4">
        <form role="search" onSubmit={onSubmit} className="flex gap-2">
          <Label htmlFor="query" className="sr-only">
            Search code
          </Label>
          <Input
            id="query"
            ref={queryInput}
            type="search"
            autoComplete="off"
            spellCheck={false}
            placeholder="Search indexed code"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={onQueryKeyDown}
          />
          <Button type="submit" id="search-button">
            <Search aria-hidden="true" /> Search
          </Button>
          <Button type="button" variant="outline" size="icon" aria-label="Query syntax" aria-expanded={syntaxOpen} onClick={openSyntax}>
            <CircleHelp aria-hidden="true" />
          </Button>
        </form>
        <div className="grid gap-6 lg:grid-cols-[232px_minmax(0,1fr)]">
          <div className="grid content-start gap-5">
            <section className="grid gap-2">
              <h2 className="text-xs font-medium text-muted-foreground uppercase">Repositories</h2>
              <RepositoryScope />
            </section>
            <section className="grid gap-2">
              <Label htmlFor="language-filter" className="text-xs font-medium text-muted-foreground uppercase">
                Language
              </Label>
              <Select value={language || anyLanguage} onValueChange={(value) => setLanguage(value === anyLanguage ? '' : value)}>
                <SelectTrigger id="language-filter" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={anyLanguage}>All languages</SelectItem>
                  {languages.map((name) => (
                    <SelectItem key={name} value={name}>
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </section>
            <section className="grid gap-2">
              <h2 className="text-xs font-medium text-muted-foreground uppercase">Examples</h2>
              {examples.map((text) => (
                <Button key={text} type="button" variant="outline" size="sm" className="justify-start" onClick={() => applyExample(text)}>
                  <code className="font-mono text-xs">{text}</code>
                </Button>
              ))}
              <Button type="button" variant="link" size="sm" className="justify-start px-0" onClick={openSyntax}>
                Query syntax →
              </Button>
            </section>
          </div>
          <div className="grid min-w-0 content-start gap-4">
            <div className="flex flex-wrap items-baseline gap-x-3">
              <h1 className="text-xl font-semibold">{search.data ? countLabel(search.data.matches.length, 'match') : 'No search yet'}</h1>
              <span className="text-sm text-muted-foreground">{search.data ? countLabel(groups.length, 'repository') : 'All authorized repositories'}</span>
              {search.data?.truncated && <span className="text-sm text-muted-foreground">Results truncated</span>}
              <p role="status" aria-live="polite" aria-atomic="true" className={search.data ? 'sr-only' : 'w-full text-sm text-muted-foreground'}>
                {status}
              </p>
            </div>
            {error && <ErrorAlert error={error} onRetry={() => void search.refetch()} />}
            {search.isFetching && !search.data && <Skeleton className="h-32 w-full" aria-label="Searching" />}
            {search.data && search.data.matches.length === 0 && <p className="text-sm text-muted-foreground">No matches. Try file:.go NewService</p>}
            {search.data && (
              <ResultsList groups={groups} fileReads={file_reads} onOpen={(target, trigger) => openFile(target, trigger)} />
            )}
          </div>
        </div>
      </div>
      {file && <FileViewer target={file} onBack={closeFile} onOpen={(target) => openFile(target)} />}
      <SyntaxDrawer
        open={syntaxOpen}
        onOpenChange={setSyntaxOpen}
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          const opener = syntaxOpener.current
          syntaxOpener.current = null
          ;(opener?.isConnected ? opener : queryInput.current)?.focus()
        }}
      />
    </>
  )
}
