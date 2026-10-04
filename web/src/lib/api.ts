import type { ErrorResponse } from '@/api/types'

export class ApiError extends Error {
  readonly status: number
  readonly body?: ErrorResponse

  constructor(status: number, body?: ErrorResponse) {
    super(body?.error.message || `Request failed (${status}).`)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

let bearerToken = ''
let onUnauthorized: () => void = () => {}

/** The auth provider owns the token; the client only attaches it. */
export function setBearerToken(token: string) {
  bearerToken = token
}

/** Called after any 401 so the auth provider can clear the token and show the gate. */
export function setUnauthorizedHandler(handler: () => void) {
  onUnauthorized = handler
}

export interface RequestOptions {
  method?: string
  body?: unknown
  signal?: AbortSignal
  /** Do not treat a 401 as a lost credential (session and sign-in probes). */
  keepCredentialOn401?: boolean
}

async function parseBody(response: Response): Promise<unknown> {
  const text = await response.text()
  if (!text) return undefined
  try {
    return JSON.parse(text)
  } catch {
    return undefined
  }
}

function isErrorResponse(value: unknown): value is ErrorResponse {
  const error = (value as ErrorResponse | undefined)?.error
  return typeof error === 'object' && error !== null && typeof error.message === 'string'
}

/** Sends a request and returns the parsed JSON body, or undefined for empty bodies such as 204. */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (bearerToken) headers.Authorization = `Bearer ${bearerToken}`
  if (options.body !== undefined) headers['Content-Type'] = 'application/json'
  const response = await fetch(path, {
    method: options.method ?? 'GET',
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    credentials: 'same-origin',
    cache: 'no-store',
    signal: options.signal,
  })
  const body = await parseBody(response)
  if (!response.ok) {
    if (response.status === 401 && !options.keepCredentialOn401) onUnauthorized()
    throw new ApiError(response.status, isErrorResponse(body) ? body : undefined)
  }
  return body as T
}
