import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { TableCell, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'
import { errorMessage, partialNotice, toneOf, type Tone } from './format'

const toneClass: Record<Tone, string> = {
  ok: 'border-emerald-600/40 text-emerald-700 dark:text-emerald-400',
  warn: 'border-amber-600/40 text-amber-700 dark:text-amber-400',
  err: 'border-destructive/50 text-destructive',
  unknown: 'text-muted-foreground',
}

/** The status text is always shown; colour only reinforces it. */
export function StatusPill({ value }: { value: string }) {
  return (
    <Badge variant="outline" className={toneClass[toneOf(value)]}>
      {value || 'unknown'}
    </Badge>
  )
}

export function PageHeader({ title, subtitle }: { title: string; subtitle: string }) {
  return (
    <div className="grid gap-1">
      <h1 className="text-xl font-semibold">{title}</h1>
      <p className="text-sm text-muted-foreground">{subtitle}</p>
    </div>
  )
}

export function StatCard({ label, value, tone }: { label: string; value: string | number; tone?: 'warn' | 'err' }) {
  return (
    <Card className="gap-1 py-4">
      <CardHeader className="px-4">
        <p className="text-sm text-muted-foreground">{label}</p>
      </CardHeader>
      <CardContent className="px-4">
        <strong className={cn('text-2xl', tone === 'warn' && 'text-amber-700 dark:text-amber-400', tone === 'err' && 'text-destructive')}>{value}</strong>
      </CardContent>
    </Card>
  )
}

export function Panel({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-2">
        <CardTitle className="text-sm">{title}</CardTitle>
        {action}
      </CardHeader>
      <CardContent className="grid gap-2 text-sm">{children}</CardContent>
    </Card>
  )
}

export function ErrorNotice({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  return (
    <Alert variant="destructive" aria-live="assertive">
      <AlertDescription>
        <p>{errorMessage(error)}</p>
        {onRetry && (
          <Button type="button" size="sm" variant="outline" onClick={onRetry}>
            Retry
          </Button>
        )}
      </AlertDescription>
    </Alert>
  )
}

export function PartialNotice({ names }: { names: string[] }) {
  return (
    <Alert aria-live="polite">
      <AlertDescription>{partialNotice(names)}</AlertDescription>
    </Alert>
  )
}

/** Skeleton while loading, Alert on failure, otherwise the data. A failed refresh keeps showing the last data under the Alert. */
export function QueryBoundary<T>({
  query,
  label,
  children,
}: {
  query: UseQueryResult<T>
  label: string
  children: (data: T) => ReactNode
}) {
  if (query.data === undefined) {
    return query.isError ? (
      <ErrorNotice error={query.error} onRetry={() => void query.refetch()} />
    ) : (
      <Skeleton className="h-32 w-full" aria-label={`Loading ${label}`} />
    )
  }
  return (
    <>
      {query.isError && <ErrorNotice error={query.error} />}
      {children(query.data)}
    </>
  )
}

export function EmptyRow({ columns, children }: { columns: number; children: ReactNode }) {
  return (
    <TableRow>
      <TableCell colSpan={columns} className="py-6 text-center text-muted-foreground">
        {children}
      </TableCell>
    </TableRow>
  )
}

export const mono = 'font-mono break-all whitespace-normal'
