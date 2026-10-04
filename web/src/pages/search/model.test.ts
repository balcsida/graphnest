import { describe, expect, it } from 'vitest'
import { ApiError } from '@/lib/api'
import type { ScipLocation, SearchMatch } from '@/api/types'
import {
  blobUrl,
  countLabel,
  describeError,
  formatIndexedAt,
  groupMatches,
  locationTarget,
  navigationRequest,
  queryWithLanguage,
  repositoryUrl,
  tokenizeLine,
} from './model'

const repository = (name: string) => ({ id: 1, name, branch: 'main', indexed_sha: '', web_url: `https://github.com/${name}` })
const match = (name: string, path: string, line: number, sha = '0123456789abcdef'): SearchMatch => ({
  repository: repository(name),
  path,
  sha,
  line_number: line,
  line_start: line,
  line_end: line,
  preview: 'x',
  score: 1,
})

describe('groupMatches', () => {
  it('groups by repository then file, keeping order, with counts and the first short-SHA source', () => {
    const groups = groupMatches([match('acme/one', 'a.go', 1), match('acme/two', 'b.go', 2, 'f'.repeat(40)), match('acme/one', 'a.go', 9), match('acme/one', 'c.go', 3)])
    expect(groups.map((g) => [g.name, g.matchCount, g.files.map((f) => [f.path, f.matches.length])])).toEqual([
      ['acme/one', 3, [['a.go', 2], ['c.go', 1]]],
      ['acme/two', 1, [['b.go', 1]]],
    ])
    expect(groups[0].sha).toBe('0123456789abcdef')
  })
})

describe('blobUrl', () => {
  const base = { web_url: 'https://github.example/acme/one', sha: '0123456789abcdef0123456789abcdef01234567', path: 'main.go', line: 1 }

  it('builds the exact SHA, path and line link', () => {
    expect(blobUrl(base)).toBe('https://github.example/acme/one/blob/0123456789abcdef0123456789abcdef01234567/main.go#L1')
  })

  it('URL-encodes each path segment and the SHA, keeping slashes', () => {
    expect(blobUrl({ ...base, sha: 'a b', path: 'dir one/file #1?.go' })).toBe('https://github.example/acme/one/blob/a%20b/dir%20one/file%20%231%3F.go#L1')
  })

  it('drops a trailing slash and omits the anchor without a line', () => {
    expect(blobUrl({ ...base, web_url: 'https://github.example/acme/one/', line: 0 })).toBe(
      'https://github.example/acme/one/blob/0123456789abcdef0123456789abcdef01234567/main.go',
    )
  })

  it('requires https and a parseable URL', () => {
    expect(blobUrl({ ...base, web_url: 'http://github.example/acme/one' })).toBeNull()
    expect(blobUrl({ ...base, web_url: 'javascript:alert(1)' })).toBeNull()
    expect(blobUrl({ ...base, web_url: '' })).toBeNull()
  })

  it('builds a location link from a SCIP location', () => {
    const location = { repository_id: 2, repository_name: 'acme/two', branch: 'dev', web_url: 'https://github.example/acme/two', commit: 'c'.repeat(40), path: 'x/y.go', start_line: 7 } as ScipLocation
    expect(locationTarget(location)).toEqual({ repository: { id: 2, name: 'acme/two', branch: 'dev', web_url: 'https://github.example/acme/two' }, sha: 'c'.repeat(40), path: 'x/y.go', line: 7 })
  })

  it('accepts only https repository links', () => {
    expect(repositoryUrl('https://github.com/a/b')).toBe('https://github.com/a/b')
    expect(repositoryUrl('http://github.com/a/b')).toBeNull()
    expect(repositoryUrl(undefined)).toBeNull()
  })
})

describe('countLabel', () => {
  it('pluralizes matches and repositories', () => {
    expect(countLabel(1, 'match')).toBe('1 match')
    expect(countLabel(0, 'match')).toBe('0 matches')
    expect(countLabel(2, 'match')).toBe('2 matches')
    expect(countLabel(1, 'repository')).toBe('1 repository')
    expect(countLabel(2, 'repository')).toBe('2 repositories')
  })
})

describe('queryWithLanguage', () => {
  it('prefixes the language unless the query already has lang:', () => {
    expect(queryWithLanguage('NewService', 'go')).toBe('lang:go NewService')
    expect(queryWithLanguage('lang:python x', 'go')).toBe('lang:python x')
    expect(queryWithLanguage('NewService', '')).toBe('NewService')
  })
})

describe('tokenizeLine and navigationRequest', () => {
  it('reports UTF-8, UTF-16 and UTF-32 offsets for identifiers after multibyte text', () => {
    // "é" is 2 UTF-8 bytes; "😀" is 4 UTF-8 bytes, 2 UTF-16 units and 1 code point.
    const segments = tokenizeLine('é😀 value')
    const identifiers = segments.filter((s) => s.identifier)
    expect(identifiers.map((s) => s.text)).toEqual(['é', 'value'])
    expect(identifiers[1]).toMatchObject({ offsets: { character_utf8: 7, character_utf16: 4, character_utf32: 3 } })
  })

  it('keeps non-identifier text between identifiers', () => {
    expect(tokenizeLine('a.b(1)').map((s) => s.text)).toEqual(['a', '.', 'b', '(1)'])
  })

  it('builds the navigation body with every offset, the commit and the operation', () => {
    const target = { repository: repository('acme/one'), sha: 'c'.repeat(40), path: 'main.go', line: 3 }
    expect(navigationRequest(target, 5, { character_utf8: 4, character_utf16: 3, character_utf32: 2 }, 'references')).toEqual({
      repository_id: 1,
      path: 'main.go',
      commit: 'c'.repeat(40),
      line: 5,
      character_utf8: 4,
      character_utf16: 3,
      character_utf32: 2,
      operation: 'references',
    })
  })
})

describe('describeError and formatIndexedAt', () => {
  it('uses the structured message, request ID and retryable flag', () => {
    const error = new ApiError(503, { error: { code: 'unavailable', message: ' Search backend is down. ', request_id: 'req-1', retryable: true } })
    expect(describeError(error)).toEqual({ message: 'Search backend is down.', requestId: 'req-1', retryable: true })
  })

  it('falls back by status', () => {
    expect(describeError(new ApiError(502)).message).toBe('The service is unavailable.')
    expect(describeError(new ApiError(400)).message).toBe('The request could not be completed.')
  })

  it('formats missing and invalid dates', () => {
    expect(formatIndexedAt(undefined)).toBe('Never')
    expect(formatIndexedAt('nope')).toBe('Unknown')
    expect(formatIndexedAt('2026-01-02T03:04:00Z')).not.toBe('Unknown')
  })
})
