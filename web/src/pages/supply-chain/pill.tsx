import { Badge } from '@/components/ui/badge'
import { licenseTone, outcomeTone, statusTone, text, type Tone } from './format'

const toneClass: Record<Tone, string> = {
  ok: 'border-emerald-600/40 text-emerald-700 dark:text-emerald-400',
  warn: 'border-amber-600/40 text-amber-700 dark:text-amber-400',
  err: 'border-destructive/50 text-destructive',
  unknown: 'text-muted-foreground',
}

/** The value is always shown as text; colour only reinforces it. `label` is read before it by screen readers. */
export function Pill({ value, tone, label }: { value: string | undefined; tone: Tone; label?: string }) {
  const shown = value || 'unknown'
  return (
    <Badge variant="outline" className={toneClass[tone]} data-tone={tone}>
      {label && <span className="sr-only">{label}: </span>}
      {shown}
    </Badge>
  )
}

export const StatusPill = ({ value, label }: { value: string | undefined; label?: string }) => <Pill value={value} tone={statusTone(value)} label={label} />

export const OutcomePill = ({ value, label }: { value: string | undefined; label?: string }) => <Pill value={value} tone={outcomeTone(value)} label={label} />

export const ScopePill = ({ value }: { value: string | undefined }) => <Pill value={value} tone="unknown" label="Scope" />

/** Assessment status, then its expression in monospace when there is one. */
export function AssessmentCell({ status, expression, label }: { status: string | undefined; expression?: string; label: string }) {
  if (status === undefined && !expression) return <>{text(undefined)}</>
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <Pill value={status} tone={licenseTone(status)} label={label} />
      {expression && <span className="font-mono break-all">{expression}</span>}
    </span>
  )
}
