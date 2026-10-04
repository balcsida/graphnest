import type { Repository, ScipLocation, ScipNavigationRequest, ScipOperation, SearchMatch } from '@/api/types'
import { ApiError } from '@/lib/api'

export type RepositoryRef = Pick<Repository, 'id' | 'name' | 'branch' | 'web_url'>

/** What the file viewer opens: a search match or a SCIP location. `line` is zero when unknown. */
export interface FileTarget {
  repository: RepositoryRef
  sha: string
  path: string
  line: number
}

export const countLabel = (count: number, noun: 'match' | 'repository') =>
  `${count} ${count === 1 ? noun : noun === 'match' ? 'matches' : 'repositories'}`

export const shortSha = (sha: string) => sha.slice(0, 7)

/** Indexed-source link on the repository host; https only, SHA and path URL-encoded per segment. */
export function blobUrl(target: { web_url: string; sha: string; path: string; line?: number }): string | null {
  try {
    const url = new URL(target.web_url)
    if (url.protocol !== 'https:') return null
    const encode = (value: string) => value.split('/').map(encodeURIComponent).join('/')
    return `${url.href.replace(/\/$/, '')}/blob/${encode(target.sha)}/${encode(target.path)}${target.line ? `#L${target.line}` : ''}`
  } catch {
    return null
  }
}

export const targetUrl = (target: FileTarget) =>
  blobUrl({ web_url: target.repository.web_url, sha: target.sha, path: target.path, line: target.line })

export function repositoryUrl(webUrl: string | undefined): string | null {
  if (!webUrl) return null
  try {
    const url = new URL(webUrl)
    return url.protocol === 'https:' ? url.href : null
  } catch {
    return null
  }
}

export function formatIndexedAt(value: string | undefined): string {
  if (!value) return 'Never'
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? 'Unknown' : new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

export const matchTarget = (match: SearchMatch): FileTarget => ({
  repository: match.repository,
  sha: match.sha,
  path: match.path,
  line: match.line_number,
})

export const locationTarget = (location: ScipLocation): FileTarget => ({
  repository: { id: location.repository_id, name: location.repository_name, branch: location.branch, web_url: location.web_url },
  sha: location.commit,
  path: location.path,
  line: location.start_line,
})

/** Prefixes the language filter unless the query already names a language. */
export function queryWithLanguage(query: string, language: string): string {
  return language && !/(?:^|\s)lang:\S+/.test(query) ? `lang:${language} ${query}` : query
}

export interface RepositoryGroup {
  name: string
  sha: string
  matchCount: number
  files: { path: string; matches: SearchMatch[] }[]
}

/** Groups matches by repository, then file, keeping first-seen order. */
export function groupMatches(matches: SearchMatch[]): RepositoryGroup[] {
  const repositories = new Map<string, Map<string, SearchMatch[]>>()
  for (const match of matches) {
    let files = repositories.get(match.repository.name)
    if (!files) repositories.set(match.repository.name, (files = new Map()))
    const entries = files.get(match.path)
    if (entries) entries.push(match)
    else files.set(match.path, [match])
  }
  return [...repositories].map(([name, files]) => {
    const entries = [...files].map(([path, found]) => ({ path, matches: found }))
    return { name, sha: entries[0].matches[0].sha, matchCount: matches.filter((m) => m.repository.name === name).length, files: entries }
  })
}

export const previewLines = (preview: string) => preview.replace(/\n$/, '').split('\n')

export interface IdentifierOffsets {
  character_utf8: number
  character_utf16: number
  character_utf32: number
}

export type LineSegment = { text: string; identifier?: undefined } | { text: string; identifier: true; offsets: IdentifierOffsets }

/** Splits a source line into plain text and identifiers carrying their UTF-8, UTF-16 and UTF-32 offsets. */
export function tokenizeLine(text: string): LineSegment[] {
  const segments: LineSegment[] = []
  const encoder = new TextEncoder()
  let cursor = 0
  for (const found of text.matchAll(/[\p{L}_$][\p{L}\p{N}_$]*/gu)) {
    if (found.index > cursor) segments.push({ text: text.slice(cursor, found.index) })
    const prefix = text.slice(0, found.index)
    segments.push({
      text: found[0],
      identifier: true,
      offsets: { character_utf8: encoder.encode(prefix).length, character_utf16: prefix.length, character_utf32: Array.from(prefix).length },
    })
    cursor = found.index + found[0].length
  }
  if (cursor < text.length) segments.push({ text: text.slice(cursor) })
  return segments
}

export const navigationRequest = (
  target: FileTarget,
  line: number,
  offsets: IdentifierOffsets,
  operation: ScipOperation,
): ScipNavigationRequest => ({
  repository_id: target.repository.id,
  path: target.path,
  commit: target.sha,
  line,
  ...offsets,
  operation,
})

/** An error whose message is already written for the user. */
export class UserFacingError extends Error {}

/** The indexed revision moved between the search and the file read. */
export class IndexedRevisionChangedError extends UserFacingError {
  constructor() {
    super('Indexed revision changed. Search again.')
  }
}

/** A search was submitted with every repository unchecked. */
export class EmptyScopeError extends UserFacingError {
  constructor() {
    super('Choose at least one repository.')
  }
}

export interface DescribedError {
  message: string
  requestId?: string
  retryable: boolean
}

/** Structured API error text: the server message when present, otherwise a status-based fallback. */
export function describeError(error: unknown): DescribedError {
  if (error instanceof ApiError) {
    const detail = error.body?.error
    return {
      message: detail?.message.trim() || (error.status >= 500 ? 'The service is unavailable.' : 'The request could not be completed.'),
      requestId: detail?.request_id || undefined,
      retryable: Boolean(detail?.retryable),
    }
  }
  if (error instanceof UserFacingError) return { message: error.message, retryable: false }
  return { message: 'The request could not be completed.', retryable: false }
}

/** Groups SCIP locations by repository name, keeping response order. */
export function groupLocations(locations: ScipLocation[]): { repository: string; locations: ScipLocation[] }[] {
  const groups = new Map<string, ScipLocation[]>()
  for (const location of locations) {
    const found = groups.get(location.repository_name)
    if (found) found.push(location)
    else groups.set(location.repository_name, [location])
  }
  return [...groups].map(([repository, found]) => ({ repository, locations: found }))
}
