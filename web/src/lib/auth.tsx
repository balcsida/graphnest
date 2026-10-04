import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { getAuthConfig, getAuthSession, logout } from '@/api/auth'
import type { AuthConfig, AuthProvider as LoginProvider, AuthSession } from '@/api/types'
import { ApiError, setBearerToken, setUnauthorizedHandler } from '@/lib/api'

const TOKEN_KEY = 'graphnest_token'
const LEGACY_ADMIN_TOKEN_KEY = 'graphnest_admin_token'

// Bearer tokens live in sessionStorage only: never localStorage, never a cookie.
function readStoredToken(): string {
  try {
    const legacy = sessionStorage.getItem(LEGACY_ADMIN_TOKEN_KEY)
    if (legacy !== null) {
      sessionStorage.removeItem(LEGACY_ADMIN_TOKEN_KEY)
      if (!sessionStorage.getItem(TOKEN_KEY) && legacy) sessionStorage.setItem(TOKEN_KEY, legacy)
    }
    return sessionStorage.getItem(TOKEN_KEY) ?? ''
  } catch {
    return ''
  }
}

function storeToken(token: string) {
  try {
    if (token) sessionStorage.setItem(TOKEN_KEY, token)
    else sessionStorage.removeItem(TOKEN_KEY)
  } catch {
    // Storage unavailable: the token then lives for this page load only.
  }
  setBearerToken(token)
}

type Status = 'loading' | 'gate' | 'authenticated'

const fallbackConfig: AuthConfig = { token_login: true, break_glass: false, file_reads: false, providers: [] }

export interface Auth {
  status: Status
  token_login: boolean
  break_glass: boolean
  file_reads: boolean
  providers: LoginProvider[]
  /** `bearer` when the stored token authenticates, otherwise the browser session method. */
  method: AuthSession['method'] | null
  /** Message from the last failed session check or token sign-in. */
  error: string
  /** Re-runs the session check, for example after a local administrator sign-in. */
  refresh: () => Promise<void>
  /** Validates a bearer token and keeps it in sessionStorage on success. */
  signInWithToken: (token: string) => Promise<void>
  /** With a session, POST /auth/logout must succeed first and a failure rejects. */
  signOut: () => Promise<void>
  /** Registers principal-scoped cleanup (file viewer, form state) to run on sign-out and 401. */
  registerPrincipalReset: (reset: () => void) => () => void
}

const AuthContext = createContext<Auth | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [config, setConfig] = useState<AuthConfig>(fallbackConfig)
  const [status, setStatus] = useState<Status>('loading')
  const [method, setMethod] = useState<AuthSession['method'] | null>(null)
  const [error, setError] = useState('')
  const resets = useRef(new Set<() => void>())

  const dropPrincipal = useCallback(() => {
    storeToken('')
    setMethod(null)
    setStatus('gate')
    queryClient.clear()
    resets.current.forEach((reset) => reset())
  }, [queryClient])

  const check = useCallback(async () => {
    try {
      setConfig(await getAuthConfig())
      setError('')
    } catch (failure) {
      setConfig(fallbackConfig)
      setError(failure instanceof Error ? failure.message : 'Unable to load sign-in options.')
    }
    // A browser session wins: probe without the bearer token first.
    setBearerToken('')
    try {
      const session = await getAuthSession()
      queryClient.clear()
      setMethod(session.method)
      setStatus('authenticated')
      return
    } catch (failure) {
      if (!(failure instanceof ApiError) || failure.status !== 401) {
        setStatus('gate')
        setError(failure instanceof Error ? failure.message : 'Unable to check the session.')
        return
      }
    }
    const token = readStoredToken()
    if (!token) {
      setMethod(null)
      setStatus('gate')
      return
    }
    setBearerToken(token)
    try {
      const session = await getAuthSession()
      setMethod(session.method)
      setStatus('authenticated')
    } catch (failure) {
      if (failure instanceof ApiError && failure.status === 401) storeToken('')
      else setBearerToken('')
      setMethod(null)
      setStatus('gate')
    }
  }, [queryClient])

  useEffect(() => {
    setUnauthorizedHandler(dropPrincipal)
    // Initial session check; its state updates arrive after awaited requests.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void check()
    return () => setUnauthorizedHandler(() => {})
  }, [check, dropPrincipal])

  const signInWithToken = useCallback(
    async (token: string) => {
      storeToken(token)
      try {
        const session = await getAuthSession()
        queryClient.clear()
        setError('')
        setMethod(session.method)
        setStatus('authenticated')
      } catch (failure) {
        storeToken('')
        throw failure
      }
    },
    [queryClient],
  )

  const signOut = useCallback(async () => {
    if (method !== 'bearer') await logout()
    dropPrincipal()
  }, [method, dropPrincipal])

  const registerPrincipalReset = useCallback((reset: () => void) => {
    resets.current.add(reset)
    return () => {
      resets.current.delete(reset)
    }
  }, [])

  const value = useMemo<Auth>(
    () => ({
      status,
      token_login: config.token_login,
      break_glass: config.break_glass,
      file_reads: config.file_reads,
      providers: config.providers,
      method,
      error,
      refresh: check,
      signInWithToken,
      signOut,
      registerPrincipalReset,
    }),
    [status, config, method, error, check, signInWithToken, signOut, registerPrincipalReset],
  )
  return <AuthContext value={value}>{children}</AuthContext>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): Auth {
  const value = useContext(AuthContext)
  if (!value) throw new Error('useAuth must be used inside AuthProvider')
  return value
}
