import type { ReactNode } from 'react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { DEFAULT_STREAM } from '@/api/supply-chain'
import { messageOf } from './format'

export function ViewHeader({ title, description = 'Observed inventory, exactly as the producer reported it.' }: { title: string; description?: string }) {
  return (
    <div className="grid gap-1">
      <h1 className="text-xl font-semibold">{title}</h1>
      <p className="text-sm text-muted-foreground">{description}</p>
    </div>
  )
}

/** A count with the denominator it is measured against. */
export function MetricCard({ label, value, denominator }: { label: string; value: ReactNode; denominator: string }) {
  return (
    <Card className="h-full gap-1 py-4">
      <CardHeader className="px-4">
        <p className="text-sm text-muted-foreground">{label}</p>
      </CardHeader>
      <CardContent className="grid gap-1 px-4">
        <strong className="text-3xl leading-none font-semibold tabular-nums">{value}</strong>
        <small className="text-muted-foreground">{denominator}</small>
      </CardContent>
    </Card>
  )
}

export interface Choice {
  value: string
  label: string
}

export function ChoiceSelect({
  id,
  label,
  value,
  choices,
  onChange,
  disabled,
  className,
}: {
  id: string
  label: string
  value: string
  choices: Choice[]
  onChange: (value: string) => void
  disabled?: boolean
  className?: string
}) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Select value={value} onValueChange={onChange} disabled={disabled}>
        <SelectTrigger id={id} className={className ?? 'w-64 max-w-full'}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {choices.map((choice) => (
            <SelectItem key={choice.value} value={choice.value}>
              {choice.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

/** Shared by every view; a stream chosen through the URL that is not the default stays selectable. */
export function StreamSelect({ stream, onChange }: { stream: string; onChange: (stream: string) => void }) {
  const choices: Choice[] = [{ value: DEFAULT_STREAM, label: 'GitHub dependency graph (source observation)' }]
  if (stream !== DEFAULT_STREAM) choices.push({ value: stream, label: stream })
  return <ChoiceSelect id="sc-stream" label="Stream" value={stream} choices={choices} onChange={onChange} className="w-96 max-w-full" />
}

/** The overview answered 404: the module is switched off on this server. */
export function DisabledModule({ error }: { error: unknown }) {
  return (
    <Alert role="alert">
      <AlertTitle>Dependencies &amp; Licenses is not available</AlertTitle>
      <AlertDescription>
        <p>{messageOf(error)}</p>
        <p>The inventory is disabled unless GRAPHNEST_SUPPLY_CHAIN=true is set on the server.</p>
      </AlertDescription>
    </Alert>
  )
}

export function LoadMore({ visible, loading, onClick }: { visible: boolean; loading: boolean; onClick: () => void }) {
  if (!visible) return null
  return (
    <Button type="button" variant="secondary" className="justify-self-start" disabled={loading} onClick={onClick}>
      Load more
    </Button>
  )
}

/** Polite status line: loading, error and empty messages are announced. */
export function StateMessage({ children }: { children: ReactNode }) {
  return (
    <p role="status" aria-live="polite" className="text-sm text-muted-foreground">
      {children}
    </p>
  )
}

/** Label and value pairs. */
export function KeyValues({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
      {rows.map(([label, value]) => (
        <div key={label} className="contents">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="break-all">{value}</dd>
        </div>
      ))}
    </dl>
  )
}
