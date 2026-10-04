import { useRef, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { getSupplyChainRepositoryComponent } from '@/api/supply-chain'
import type { SupplyChainLicenseEvidence } from '@/api/types'
import { ErrorNotice, mono } from '@/pages/admin/shared'
import { DASH, licenseTone, text, when, yesNo } from './format'
import { OutcomePill, Pill } from './pill'
import { KeyValues } from './shared'

/** Evidence tables are bounded by the API (100 rows); a response that ignores the bound is cut to it. */
const ROW_LIMIT = 100
const NOTE_LIMIT = 50
const FINGERPRINT_LENGTH = 12

const resolverText = (row: SupplyChainLicenseEvidence) => `resolver v${text(row.resolver_version)} · SPDX list ${text(row.license_list_version)}`

/** Only https links are followed; anything else is shown as text. */
function isHttps(value: string) {
  try {
    return new URL(value).protocol === 'https:'
  } catch {
    return false
  }
}

function LicenseLink({ row }: { row: SupplyChainLicenseEvidence }) {
  const { license_url: url, license_file_name: file } = row
  if (!url && !file) return DASH
  return (
    <>
      {url && (isHttps(url) ? (
        <a href={url} target="_blank" rel="noopener noreferrer" className="underline underline-offset-4">
          {url}
        </a>
      ) : (
        url
      ))}
      {url && file && ' · '}
      {file}
    </>
  )
}

function EvidenceTable({ columns, rows }: { columns: string[]; rows: ReactNode[][] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          {columns.map((column) => (
            <TableHead key={column} scope="col">
              {column}
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((cells, index) => (
          <TableRow key={index}>
            {cells.map((cell, cellIndex) => (
              <TableCell key={cellIndex} className="align-top whitespace-normal">
                {cell}
              </TableCell>
            ))}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

/** Evidence detail for one occurrence of a repository's snapshot. Escape and the close button call `onClose`; focus moves to the title. */
export function ComponentEvidenceSheet({
  repositoryId,
  element,
  snapshotId,
  stream,
  onClose,
}: {
  repositoryId: number
  /** Document element ID; an empty string keeps the sheet closed. */
  element: string
  snapshotId?: number
  stream: string
  onClose: () => void
}) {
  const title = useRef<HTMLHeadingElement>(null)
  const query = useQuery({
    queryKey: ['supply-chain', 'evidence', repositoryId, element, stream, snapshotId],
    queryFn: ({ signal }) => getSupplyChainRepositoryComponent(repositoryId, { element, stream, snapshot_id: snapshotId }, signal),
    enabled: element !== '',
    retry: false,
  })
  const detail = query.data
  const component = detail?.component
  const license = component?.license

  return (
    <Sheet open={element !== ''} onOpenChange={(open) => !open && onClose()}>
      <SheetContent
        className="w-full overflow-y-auto sm:max-w-5xl"
        onOpenAutoFocus={(event) => {
          event.preventDefault()
          title.current?.focus()
        }}
      >
        <SheetHeader>
          <SheetTitle ref={title} tabIndex={-1}>
            {component ? `Component evidence · ${text(component.name)}` : 'Component evidence'}
          </SheetTitle>
          <SheetDescription>Evidence is not approval: assessments describe what was collected, not a compliance verdict.</SheetDescription>
        </SheetHeader>
        <div className="grid gap-3 px-4 pb-4 text-sm">
          {query.isPending && (
            <>
              <p role="status" className="text-muted-foreground">
                Loading evidence…
              </p>
              <Skeleton className="h-24 w-full" aria-label="Loading evidence" />
            </>
          )}
          {query.isError && <ErrorNotice error={query.error} />}
          {detail && component && (
            <>
              <KeyValues
                rows={[
                  ['Name', text(component.name)],
                  ['Version', text(component.version)],
                  ['PURL', text(component.purl)],
                  ['Ecosystem', text(component.ecosystem)],
                  ['Element', text(component.element_id)],
                  ['Scope', text(component.scope)],
                  ['Root', yesNo(component.is_root)],
                ]}
              />
              <h3 className="font-medium">Assessment</h3>
              {!license ? (
                <p className="text-muted-foreground">No assessment has been recorded for this occurrence.</p>
              ) : (
                <KeyValues
                  rows={[
                    ['Status', <Pill key="s" value={license.status} tone={licenseTone(license.status)} label="Assessed license" />],
                    ...(license.expression ? ([['Expression', <span key="e" className="font-mono">{license.expression}</span>]] as [string, ReactNode][]) : []),
                    ...(license.conflict_detail ? ([['Conflict', license.conflict_detail]] as [string, ReactNode][]) : []),
                    ['Evidence rows', license.evidence_count || 0],
                    ['Assessed at', when(license.assessed_at)],
                    [
                      'Evidence fingerprint',
                      license.evidence_fingerprint ? (
                        <span key="f" className="font-mono" title={license.evidence_fingerprint}>
                          {license.evidence_fingerprint.slice(0, FINGERPRINT_LENGTH)}
                        </span>
                      ) : (
                        DASH
                      ),
                    ],
                  ]}
                />
              )}
              <h3 className="font-medium">Producer declarations</h3>
              {detail.declarations.length ? (
                <EvidenceTable
                  columns={['Source', 'Raw value', 'Parse status']}
                  rows={detail.declarations.slice(0, ROW_LIMIT).map((row) => [text(row.source), <span key="r" className={mono}>{text(row.raw_value)}</span>, text(row.parse_status)])}
                />
              ) : (
                <p className="text-muted-foreground">No producer declarations for this component.</p>
              )}
              <h3 className="font-medium">Registry evidence</h3>
              {detail.evidence.length ? (
                <EvidenceTable
                  columns={['Fetched', 'Source', 'Route', 'Outcome', 'Raw kind', 'Raw value', 'Expression', 'Parse status', 'Message', 'License link', 'Resolver']}
                  rows={detail.evidence.slice(0, ROW_LIMIT).map((row) => [
                    when(row.fetched_at),
                    text(row.source),
                    text(row.route),
                    <OutcomePill key="o" value={row.outcome} label="Outcome" />,
                    text(row.raw_kind),
                    <span key="r" className={mono}>{text(row.raw_value)}</span>,
                    text(row.expression),
                    text(row.parse_status),
                    text(row.message),
                    <span key="l" className={mono}><LicenseLink row={row} /></span>,
                    resolverText(row),
                  ])}
                />
              ) : (
                <p className="text-muted-foreground">No registry evidence for these coordinates.</p>
              )}
              <h3 className="font-medium">Relationships</h3>
              {detail.relationships.length ? (
                detail.relationships.slice(0, ROW_LIMIT).map((edge, index) => (
                  <p key={index} className="font-mono break-all">
                    {`${text(edge.from)} → ${text(edge.type)} → ${text(edge.to)}${edge.resolved === false ? ' (unresolved)' : ''}`}
                  </p>
                ))
              ) : (
                <p className="text-muted-foreground">No relationships reference this component.</p>
              )}
              {detail.notes.slice(0, NOTE_LIMIT).map((note, index) => (
                <p key={index} className="text-muted-foreground">
                  {note}
                </p>
              ))}
              {detail.truncated && <p className="text-muted-foreground">Evidence list is truncated; not every row is shown.</p>}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
