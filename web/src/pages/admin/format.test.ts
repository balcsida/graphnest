import { describe, expect, it } from 'vitest'
import { ApiError } from '@/lib/api'
import { DASH, accessLabel, directAccessLabel, errorMessage, parseId, parseIds, short, toneOf, when } from './format'

describe('admin formatting', () => {
  it('shortens to eight characters and dashes empty values', () => {
    expect(short('a'.repeat(40))).toBe('aaaaaaaa')
    expect(short('')).toBe(DASH)
    expect(when(undefined)).toBe(DASH)
    expect(when('2026-01-02T00:00:00Z')).toBe(new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date('2026-01-02T00:00:00Z')))
  })

  it('labels access with dashes for empty lists and GitHub grants', () => {
    expect(accessLabel(true, [101, 102])).toBe('Administrator · 101, 102')
    expect(accessLabel(false, [])).toBe('Standard · —')
    expect(directAccessLabel(false, [101], [5, 6])).toBe('Standard · 101 · GitHub: 5, 6')
  })

  it('parses comma-separated positive ids and rejects anything else', () => {
    expect(parseIds('101, 102,')).toEqual([101, 102])
    expect(parseIds('')).toEqual([])
    expect(parseIds('1,x')).toBeNull()
    expect(parseIds('0')).toBeNull()
    expect(parseId('7')).toBe(7)
    expect(parseId('7,8')).toBeNull()
    expect(parseId('')).toBeNull()
  })

  it('never colours an unknown status as healthy', () => {
    expect(toneOf('mystery')).toBe('unknown')
    expect(toneOf('Ready')).toBe('ok')
    expect(toneOf('failed')).toBe('err')
    expect(toneOf('queued')).toBe('warn')
  })

  it('uses the server message or the legacy HTTP fallback', () => {
    expect(errorMessage(new ApiError(503, { error: { code: 'x', message: 'Grant pagination failed.', request_id: 'r', retryable: false } }))).toBe('Grant pagination failed.')
    expect(errorMessage(new ApiError(504))).toBe('Request failed with HTTP 504.')
  })
})
