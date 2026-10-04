import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { readFile, scipNavigation } from '@/api/search'
import type { ScipOperation } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorAlert } from './ErrorAlert'
import {
  IndexedRevisionChangedError,
  groupLocations,
  locationTarget,
  navigationRequest,
  shortSha,
  targetUrl,
  tokenizeLine,
  type FileTarget,
  type IdentifierOffsets,
} from './model'

interface Selection {
  /** Identifies the file and line the identifier belongs to, so a selection never outlives its file. */
  fileKey: string
  line: number
  text: string
  offsets: IdentifierOffsets
}

const operations: { value: ScipOperation; label: string }[] = [
  { value: 'definitions', label: 'Definitions' },
  { value: 'references', label: 'References' },
  { value: 'implementations', label: 'Implementations' },
]

const fileKeyOf = (target: FileTarget) => `${target.repository.id}:${target.sha}:${target.path}:${target.line}`

function NavigationPanel({
  target,
  selection,
  operation,
  onOperation,
  onOpen,
}: {
  target: FileTarget
  selection: Selection
  operation: ScipOperation
  onOperation: (operation: ScipOperation) => void
  onOpen: (target: FileTarget) => void
}) {
  const navigation = useQuery({
    queryKey: ['scip-navigation', selection.fileKey, selection.line, selection.offsets, operation],
    queryFn: ({ signal }) => scipNavigation(navigationRequest(target, selection.line, selection.offsets, operation), signal),
    retry: false,
    staleTime: Infinity,
  })
  const data = navigation.data
  let status = ''
  if (navigation.isPending) status = 'Loading code navigation…'
  else if (data?.truncated) status = 'Locations were truncated.'
  else if (data && data.locations.length === 0) status = 'No locations found.'

  return (
    <aside aria-labelledby="navigation-title" className="grid content-start gap-3 rounded-md border p-3">
      <div className="grid gap-1">
        <h2 id="navigation-title" className="text-sm font-semibold">
          Code navigation
        </h2>
        <code className="font-mono text-sm break-all">{selection.text}</code>
      </div>
      <div role="group" aria-label="Navigation operation" className="flex flex-wrap gap-1">
        {operations.map((item) => (
          <Button
            key={item.value}
            type="button"
            size="sm"
            variant={item.value === operation ? 'default' : 'outline'}
            aria-pressed={item.value === operation}
            onClick={() => onOperation(item.value)}
          >
            {item.label}
          </Button>
        ))}
      </div>
      <p role="status" aria-live="polite" aria-atomic="true" className="text-sm text-muted-foreground">
        {status}
      </p>
      {navigation.isError && <ErrorAlert error={navigation.error} onRetry={() => void navigation.refetch()} />}
      {data &&
        groupLocations(data.locations).map((group) => (
          <section key={group.repository} className="grid gap-2">
            <h3 className="text-sm font-medium break-all">{group.repository}</h3>
            {group.locations.map((location, index) => {
              const next = locationTarget(location)
              const href = targetUrl(next)
              return (
                <div key={`${location.path}:${location.start_line}:${index}`} className="grid gap-1">
                  <Button type="button" variant="ghost" className="h-auto flex-col items-start gap-0.5 px-2 py-1.5 text-left" onClick={() => onOpen(next)}>
                    <span className="font-mono text-xs break-all whitespace-normal">
                      {`${location.path}:${location.start_line}`}
                      {location.approximate && (
                        <Badge variant="outline" className="ml-2">
                          Approximate
                        </Badge>
                      )}
                    </span>
                    <code className="font-mono text-xs break-all whitespace-normal text-muted-foreground">{location.symbol || 'Indexed symbol'}</code>
                  </Button>
                  {href && (
                    <a href={href} target="_blank" rel="noopener noreferrer" className="px-2 text-xs underline underline-offset-4">
                      Open indexed source ↗
                    </a>
                  )}
                </div>
              )
            })}
          </section>
        ))}
    </aside>
  )
}

/** In-place file viewer: line focus, identifier navigation and a locations panel. */
export function FileViewer({ target, onBack, onOpen }: { target: FileTarget; onBack: () => void; onOpen: (target: FileTarget) => void }) {
  const fileKey = fileKeyOf(target)
  const [selected, setSelected] = useState<Selection | null>(null)
  const [operation, setOperation] = useState<ScipOperation>('definitions')
  const backButton = useRef<HTMLButtonElement>(null)
  const targetRow = useRef<HTMLDivElement>(null)
  const selection = selected?.fileKey === fileKey ? selected : null

  const file = useQuery({
    queryKey: ['file', fileKey],
    queryFn: async ({ signal }) => {
      const response = await readFile(
        { repository_id: target.repository.id, path: target.path, ...(target.line ? { start_line: target.line } : {}) },
        signal,
      )
      if (response.indexed_sha !== target.sha) throw new IndexedRevisionChangedError()
      return response
    },
    retry: false,
    staleTime: Infinity,
  })

  useEffect(() => backButton.current?.focus(), [fileKey])
  useEffect(() => targetRow.current?.focus(), [file.data])

  const href = targetUrl(target)
  const revision = (target.repository.branch ? `${target.repository.branch} · ` : '') + shortSha(target.sha)
  let status = ''
  if (file.isPending) status = 'Loading indexed file…'
  else if (file.data?.truncated) status = 'File content was truncated.'

  return (
    <section aria-label="File" className="grid gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <Button ref={backButton} type="button" variant="outline" size="sm" onClick={onBack}>
          ← Results
        </Button>
        <code className="min-w-0 font-mono text-sm break-all">{`${target.repository.name} / ${target.path}`}</code>
        <Badge variant="secondary" className="max-w-full break-all whitespace-normal">
          {revision}
        </Badge>
        <p role="status" aria-live="polite" aria-atomic="true" className="text-sm text-muted-foreground">
          {status}
        </p>
        {href && (
          <a href={href} target="_blank" rel="noopener noreferrer" className="text-sm underline underline-offset-4">
            Open indexed source ↗
          </a>
        )}
      </div>
      {file.isError && <ErrorAlert error={file.error} onRetry={() => void file.refetch()} />}
      <div className={`grid gap-4 ${selection ? 'lg:grid-cols-[minmax(0,1fr)_340px]' : ''}`}>
        {file.isPending ? (
          <Skeleton className="h-64 w-full" aria-label="Loading file" />
        ) : (
          file.data && (
            <div aria-label="Indexed file contents" role="region" className="overflow-x-auto rounded-md border py-2">
              {file.data.content.split('\n').map((text, offset) => {
                const line = file.data.start_line + offset
                const isTarget = line === target.line
                return (
                  <div
                    key={line}
                    ref={isTarget ? targetRow : undefined}
                    tabIndex={isTarget ? -1 : undefined}
                    data-line={line}
                    className={`flex min-w-max ${selection?.line === line ? 'bg-accent/60' : ''}`}
                  >
                    <span className="w-14 shrink-0 pr-3 text-right font-mono text-xs leading-6 text-muted-foreground select-none">{line}</span>
                    <code className="pr-5 font-mono text-[13px] leading-6 whitespace-pre">
                      {tokenizeLine(text).map((segment, index) =>
                        segment.identifier ? (
                          <button
                            key={index}
                            type="button"
                            aria-pressed={selection?.line === line && selection.offsets.character_utf16 === segment.offsets.character_utf16}
                            className="rounded-sm hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring aria-pressed:bg-primary/15 aria-pressed:text-primary"
                            onClick={() => setSelected({ fileKey, line, text: segment.text, offsets: segment.offsets })}
                          >
                            {segment.text}
                          </button>
                        ) : (
                          segment.text
                        ),
                      )}
                    </code>
                  </div>
                )
              })}
            </div>
          )
        )}
        {selection && <NavigationPanel target={target} selection={selection} operation={operation} onOperation={setOperation} onOpen={onOpen} />}
      </div>
    </section>
  )
}
