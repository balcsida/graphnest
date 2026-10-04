import { ApiError } from '@/lib/api'
import { UserFacingError } from '@/pages/search/model'
import type { AdminOverview } from '@/api/types'

export const DASH = '—'

/** Lists are bounded to the server maximum even if a response ignores it. */
export const bounded = <T,>(values: T[] | undefined): T[] => (Array.isArray(values) ? values.slice(0, 100) : [])

export const short = (value: string | undefined) => (value ? value.slice(0, 8) : DASH)

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
export const when = (value: string | undefined) => (value ? dateTime.format(new Date(value)) : DASH)

export const idList = (ids: number[] | undefined) => (ids && ids.length ? ids.join(', ') : DASH)

export const accessLabel = (administrator: boolean, ids: number[] | undefined) =>
  `${administrator ? 'Administrator' : 'Standard'} · ${idList(ids)}`

export const directAccessLabel = (administrator: boolean, ids: number[] | undefined, github: number[] | undefined) =>
  accessLabel(administrator, ids) + (github && github.length ? ` · GitHub: ${github.join(', ')}` : '')

/** Parses "101, 102"; blank pieces are ignored. Returns null when any piece is not a positive whole number. */
export function parseIds(value: string): number[] | null {
  const ids: number[] = []
  for (const piece of value.split(',')) {
    const text = piece.trim()
    if (!text) continue
    const id = Number(text)
    if (!Number.isSafeInteger(id) || id < 1) return null
    ids.push(id)
  }
  return ids
}

export const parseId = (value: string): number | null => {
  const ids = parseIds(value)
  return ids && ids.length === 1 && !value.includes(',') ? ids[0] : null
}

/** Server message when the response carried one, otherwise the legacy fallback. */
export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.body?.error.message || `Request failed with HTTP ${error.status}.`
  if (error instanceof UserFacingError) return error.message
  return 'The request could not be completed.'
}

export const isAbort = (error: unknown) => error instanceof DOMException && error.name === 'AbortError'

export const repositoryTotal = (overview: AdminOverview) =>
  Object.values(overview.repositories ?? {}).reduce((sum, count) => sum + count, 0)

export const queueDepth = (overview: AdminOverview) => (overview.jobs?.queued ?? 0) + (overview.jobs?.running ?? 0)

export const partialNotice = (names: string[]) => `Partial inventory: ${names.join(', ')} reached the server result limit.`

export type Tone = 'ok' | 'warn' | 'err' | 'unknown'

const tones: Record<string, Tone> = {
  ready: 'ok',
  healthy: 'ok',
  succeeded: 'ok',
  active: 'ok',
  pending: 'warn',
  queued: 'warn',
  running: 'warn',
  indexing: 'warn',
  inactive: 'warn',
  failed: 'err',
  error: 'err',
  disabled: 'err',
  superseded: 'err',
  unavailable: 'err',
  suspended: 'err',
}

export const toneOf = (value: string): Tone => tones[value.toLowerCase()] ?? 'unknown'
