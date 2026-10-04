import { describe, expect, it } from 'vitest'
import { declaredText, denominatorText, enrichmentText, humanize, licenseSummaryText, licenseTone, outcomeTone, packageLabel, since, statusTone, text, when } from './format'

describe('humanize', () => {
  it('uses the largest whole unit with singular and plural grammar', () => {
    expect(humanize(172800)).toBe('2 days ago')
    expect(humanize(86400)).toBe('1 day ago')
    expect(humanize(7200)).toBe('2 hours ago')
    expect(humanize(3600)).toBe('1 hour ago')
    expect(humanize(1800)).toBe('30 minutes ago')
    expect(humanize(60)).toBe('1 minute ago')
    expect(humanize(59)).toBe('just now')
    expect(humanize(0)).toBe('just now')
  })

  it('shows a dash for an unknown or impossible age', () => {
    expect(humanize(null)).toBe('—')
    expect(humanize(undefined)).toBe('—')
    expect(humanize(Number.NaN)).toBe('—')
    expect(humanize(-5)).toBe('—')
  })

  it('measures since() from the given time', () => {
    const now = Date.parse('2026-02-01T12:00:00Z')
    expect(since('2026-02-01T10:00:00Z', now)).toBe('2 hours ago')
    expect(since(null, now)).toBe('—')
  })
})

describe('formatting', () => {
  it('summarizes assessments in the stable order', () => {
    expect(licenseSummaryText({ unknown: 2, conflict: 1, resolved: 3 })).toBe('3 resolved · 1 conflict · 2 unknown')
    expect(licenseSummaryText({})).toBe('no assessments')
    expect(licenseSummaryText({ resolved: 0 })).toBe('no assessments')
  })

  it('names the denominator and never reports a percentage', () => {
    expect(denominatorText(9, 12, 'authorized repositories')).toBe('9 of 12 authorized repositories')
    expect(denominatorText(0, 0, 'occurrences')).not.toMatch(/%|compliant/)
  })

  it('joins distinct declarations and package labels', () => {
    expect(declaredText(['NOASSERTION', 'MIT', 'MIT', ''])).toBe('NOASSERTION · MIT')
    expect(declaredText([])).toBe('—')
    expect(packageLabel('@acme', 'x')).toBe('@acme/x')
    expect(packageLabel(undefined, 'x')).toBe('x')
  })

  it('describes enrichment and missing values', () => {
    expect(enrichmentText('not_configured', ['npm'])).toBe('not configured')
    expect(enrichmentText('configured', ['npm', 'maven'])).toBe('configured for npm, maven')
    expect(enrichmentText('configured', [])).toBe('configured')
    expect(text('')).toBe('—')
    expect(text(0)).toBe('0')
    expect(when(null)).toBe('—')
    expect(when('2026-02-01T10:00:00Z')).not.toBe('—')
  })
})

describe('status tones', () => {
  it('maps collection states, outcomes and job states', () => {
    for (const value of ['current', 'published', 'unchanged', 'succeeded']) expect(statusTone(value)).toBe('ok')
    for (const value of ['stale', 'queued', 'running', 'rate_limited', 'transient']) expect(statusTone(value)).toBe('warn')
    for (const value of ['failed', 'forbidden', 'not_found', 'malformed', 'too_large', 'error', 'cancelled']) expect(statusTone(value)).toBe('err')
    expect(statusTone('never')).toBe('unknown')
    expect(statusTone('CURRENT')).toBe('ok')
  })

  it('maps assessment statuses and evidence outcomes', () => {
    expect(licenseTone('resolved')).toBe('ok')
    expect(licenseTone('conflict')).toBe('err')
    expect(licenseTone('unlicensed')).toBe('err')
    expect(licenseTone('mixed')).toBe('warn')
    expect(licenseTone('declared')).toBe('unknown')
    expect(outcomeTone('resolved')).toBe('ok')
    expect(outcomeTone('unavailable')).toBe('warn')
    expect(outcomeTone('not_found')).toBe('warn')
    expect(outcomeTone('rejected')).toBe('err')
    expect(outcomeTone('too_large')).toBe('err')
    expect(outcomeTone('malformed')).toBe('err')
    expect(outcomeTone('no_license_metadata')).toBe('unknown')
  })
})
