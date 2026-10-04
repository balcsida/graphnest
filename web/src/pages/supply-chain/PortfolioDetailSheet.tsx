import { useRef } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { getSupplyChainComponent } from '@/api/supply-chain'
import type { SupplyChainPortfolioOccurrence } from '@/api/types'
import { ErrorNotice, mono } from '@/pages/admin/shared'
import { text, when, yesNo } from './format'
import { AssessmentCell } from './pill'
import { KeyValues } from './shared'

/** Occurrences are bounded by the API; a response that ignores the bound is cut to it. */
const OCCURRENCE_LIMIT = 100

/**
 * Where one coordinate occurs in the caller's authorized repositories. `componentKey` open means the sheet is open;
 * Escape and the close button call `onClose`. Focus moves to the title, as the legacy panel did.
 */
export function PortfolioDetailSheet({
  componentKey,
  stream,
  purl,
  onClose,
  onEvidence,
}: {
  componentKey: string
  stream: string
  purl?: string
  onClose: () => void
  onEvidence: (occurrence: SupplyChainPortfolioOccurrence) => void
}) {
  const title = useRef<HTMLHeadingElement>(null)
  const query = useQuery({
    queryKey: ['supply-chain', 'component', stream, componentKey],
    queryFn: ({ signal }) => getSupplyChainComponent(componentKey, { stream }, signal),
    enabled: componentKey !== '',
    retry: false,
  })
  const detail = query.data

  return (
    <Sheet open={componentKey !== ''} onOpenChange={(open) => !open && onClose()}>
      <SheetContent
        className="w-full overflow-y-auto sm:max-w-4xl"
        onOpenAutoFocus={(event) => {
          event.preventDefault()
          title.current?.focus()
        }}
      >
        <SheetHeader>
          <SheetTitle ref={title} tabIndex={-1}>
            {detail ? `Component occurrences · ${text(detail.name)}` : 'Component occurrences'}
          </SheetTitle>
          <SheetDescription className="sr-only">Occurrences are limited to the caller&apos;s authorized repositories.</SheetDescription>
        </SheetHeader>
        <div className="grid gap-3 px-4 pb-4 text-sm">
          {query.isPending && (
            <>
              <p role="status" className="text-muted-foreground">
                Loading occurrences…
              </p>
              <Skeleton className="h-24 w-full" aria-label="Loading occurrences" />
            </>
          )}
          {query.isError && <ErrorNotice error={query.error} />}
          {detail && (
            <>
              <KeyValues
                rows={[
                  ['Ecosystem', text(detail.ecosystem)],
                  ['Namespace', text(detail.namespace)],
                  ['Name', text(detail.name)],
                  ['Version', text(detail.version)],
                  ['PURL', text(purl)],
                ]}
              />
              <h3 className="font-medium">Authorized repositories using this component</h3>
              {detail.occurrences.length === 0 ? (
                <p className="text-muted-foreground">No authorized occurrence for this coordinate.</p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      {['Repository', 'Snapshot', 'Collected', 'Element', 'Root', 'Declared', 'Assessment', 'Evidence'].map((heading) => (
                        <TableHead key={heading} scope="col">
                          {heading}
                        </TableHead>
                      ))}
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {detail.occurrences.slice(0, OCCURRENCE_LIMIT).map((occurrence) => (
                      <TableRow key={`${occurrence.repository_id}:${occurrence.snapshot_id}:${occurrence.element_id}`}>
                        <TableCell>{text(occurrence.repository)}</TableCell>
                        <TableCell>{occurrence.snapshot_id ? `#${occurrence.snapshot_id}` : '—'}</TableCell>
                        <TableCell>{when(occurrence.collected_at)}</TableCell>
                        <TableCell className={mono}>{text(occurrence.element_id)}</TableCell>
                        <TableCell>{yesNo(occurrence.root)}</TableCell>
                        <TableCell className={mono}>{text(occurrence.declared_raw)}</TableCell>
                        <TableCell>
                          <AssessmentCell status={occurrence.assessment || 'unassessed'} expression={occurrence.expression} label="Assessment" />
                        </TableCell>
                        <TableCell>
                          <Button type="button" variant="link" size="sm" onClick={() => onEvidence(occurrence)}>
                            Evidence <span className="sr-only">for {occurrence.repository} {occurrence.element_id}</span>
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
              {detail.notes.slice(0, 50).map((note, index) => (
                <p key={index} className="text-muted-foreground">
                  {note}
                </p>
              ))}
              {detail.truncated && <p className="text-muted-foreground">Occurrence list is truncated; not every row is shown.</p>}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
