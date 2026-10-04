import { vi } from 'vitest'

export interface StubbedResponse {
  status?: number
  body?: unknown
  headers?: Record<string, string>
}

export type Handler = StubbedResponse | ((request: { headers: Headers; body: unknown }) => StubbedResponse)

export interface FetchCall {
  method: string
  path: string
  headers: Headers
  body: unknown
}

/**
 * Replaces global fetch. Routes are keyed "METHOD /path" (query strings included); an unmatched
 * request answers 404 so a test only stubs what it exercises. Returns the recorded calls.
 */
export function stubFetch(routes: Record<string, Handler>) {
  const calls: FetchCall[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      const method = init?.method ?? 'GET'
      const headers = new Headers(init?.headers)
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
      calls.push({ method, path, headers, body })
      const handler = routes[`${method} ${path}`]
      const { status = 200, body: payload, headers: responseHeaders } = handler
        ? typeof handler === 'function'
          ? handler({ headers, body })
          : handler
        : { status: 404, body: { error: { code: 'not_found', message: 'Not found.', request_id: 'test', retryable: false } } }
      return new Response(payload === undefined || status === 204 ? null : JSON.stringify(payload), { status, headers: responseHeaders })
    }),
  )
  return calls
}

export const authConfig = { token_login: true, break_glass: false, file_reads: true, providers: [] }

export const unauthenticated = {
  status: 401,
  body: { error: { code: 'unauthenticated', message: 'Sign in required.', request_id: 'test', retryable: false } },
}
