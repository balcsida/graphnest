import { ApiError } from '@/lib/api'

export const DASH = '—'

/** Assessment statuses in the stable display order of the summaries; "unassessed" is only reported by the overview. */
export const LICENSE_ORDER = ['resolved', 'declared', 'conflict', 'unlicensed', 'unknown', 'pending', 'not_applicable', 'unassessed'] as const

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

export const when = (value: string | null | undefined) => (value ? dateTime.format(new Date(value)) : DASH)

/** Verbatim value, or a dash for null, undefined and the empty string. */
export const text = (value: string | number | null | undefined) => (value === null || value === undefined || value === '' ? DASH : String(value))

/** "2 hours ago" for an age in seconds; a dash when the age is unknown or negative. */
export function humanize(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds) || seconds < 0) return DASH
  for (const [unit, size] of [['day', 86400], ['hour', 3600], ['minute', 60]] as const) {
    if (seconds >= size) {
      const count = Math.floor(seconds / size)
      return `${count} ${unit}${count === 1 ? '' : 's'} ago`
    }
  }
  return 'just now'
}

export const since = (value: string | null | undefined, now = Date.now()) => (value ? humanize((now - new Date(value).getTime()) / 1000) : DASH)

/** "3 resolved · 1 conflict · 2 unknown", counts in LICENSE_ORDER, or "no assessments". */
export function licenseSummaryText(summary: Record<string, number> | null | undefined): string {
  const parts = LICENSE_ORDER.flatMap((status) => {
    const count = Number(summary?.[status])
    return Number.isFinite(count) && count > 0 ? [`${count} ${status}`] : []
  })
  return parts.length ? parts.join(' · ') : 'no assessments'
}

/** Summary entries as chips: status and count in LICENSE_ORDER. */
export const licenseSummaryEntries = (summary: Record<string, number> | null | undefined) =>
  LICENSE_ORDER.flatMap((status) => {
    const count = Number(summary?.[status])
    return Number.isFinite(count) && count > 0 ? [{ status, count }] : []
  })

export const enrichmentText = (enrichment: string, ecosystems: readonly string[] | undefined) => {
  if (enrichment !== 'configured') return 'not configured'
  const names = (ecosystems ?? []).filter(Boolean)
  return names.length ? `configured for ${names.join(', ')}` : 'configured'
}

/** A count with the denominator it is measured against, never a percentage: "9 of 12 authorized repositories". */
export const denominatorText = (count: number, total: number, noun: string) => `${count} of ${total} ${noun}`

export const packageLabel = (namespace: string | undefined, name: string) => `${namespace ? `${namespace}/` : ''}${text(name)}`

export const declaredText = (declared: readonly string[] | undefined) => [...new Set((declared ?? []).filter(Boolean))].join(' · ') || DASH

export const facetLabel = (value: string, count: number) => `${value} (${count})`

export type Tone = 'ok' | 'warn' | 'err' | 'unknown'

const ok = new Set(['current', 'published', 'unchanged', 'succeeded'])
const warn = new Set(['stale', 'queued', 'running', 'rate_limited', 'transient'])
const bad = new Set(['failed', 'forbidden', 'not_found', 'malformed', 'too_large', 'error', 'cancelled'])

/** Collection states, collection outcomes and job states. */
export const statusTone = (value: string | undefined): Tone => {
  const key = (value ?? '').toLowerCase()
  return ok.has(key) ? 'ok' : warn.has(key) ? 'warn' : bad.has(key) ? 'err' : 'unknown'
}

const licenseTones: Record<string, Tone> = { resolved: 'ok', conflict: 'err', unlicensed: 'err', mixed: 'warn' }
export const licenseTone = (value: string | undefined): Tone => licenseTones[value ?? ''] ?? 'unknown'

const outcomeTones: Record<string, Tone> = { resolved: 'ok', unavailable: 'warn', not_found: 'warn', rejected: 'err', too_large: 'err', malformed: 'err' }
export const outcomeTone = (value: string | undefined): Tone => outcomeTones[value ?? ''] ?? 'unknown'

/** Server message, or the legacy fallback. */
export const messageOf = (error: unknown) =>
  error instanceof ApiError ? error.body?.error.message || `Request failed with HTTP ${error.status}.` : 'The request could not be completed.'

export const isDisabled = (error: unknown) => error instanceof ApiError && error.status === 404

export const isAbort = (error: unknown) => error instanceof DOMException && error.name === 'AbortError'

/** Page size of every list, as in the legacy page. */
export const PAGE_LIMIT = 100

export const yesNo = (value: boolean) => (value ? 'yes' : 'no')

/** Sentinel for "no filter": Radix Select items cannot have an empty value. */
export const ANY = '__any__'
