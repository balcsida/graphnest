import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { TokenGate } from '@/components/auth/TokenGate'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { AuthProvider, useAuth } from '@/lib/auth'
import { ThemeProvider } from '@/lib/theme'
import AccountPage from '@/pages/account/AccountPage'
import AdminPage from '@/pages/admin/AdminPage'
import { authConfig, stubFetch, unauthenticated, type Handler } from '@/test/fetch'

// Test helper file: it exports a mount function next to private components.
// eslint-disable-next-line react-refresh/only-export-components
function Gate({ children }: { children: ReactNode }) {
  const { status } = useAuth()
  if (status === 'gate') return <TokenGate />
  return status === 'authenticated' ? children : null
}

export const failure = (status: number, message = 'Denied.') => ({
  status,
  body: { error: { code: 'error', message, request_id: 'test', retryable: false } },
})

export const overviewFixture = {
  repositories: { ready: 319, failed: 1 },
  jobs: { queued: 1, running: 1, succeeded: 1, failed: 1, superseded: 1 },
  deliveries: { succeeded: 1 },
  scip_uploads: 1,
  dependencies: 1,
  installations: 1,
}

/** A signed-in administrator console: every read the sections make is stubbed, overrides win. */
export function consoleRoutes(overrides: Record<string, Handler> = {}, method: 'oidc' | 'bearer' = 'oidc'): Record<string, Handler> {
  return {
    'GET /v1/auth/config': { body: authConfig },
    'GET /v1/auth/session':
      method === 'bearer' ? ({ headers }) => (headers.has('Authorization') ? { body: { method: 'bearer' } } : unauthenticated) : { body: { method } },
    'GET /v1/admin/overview': { body: overviewFixture },
    'GET /healthz': { body: {} },
    'GET /readyz': { body: {} },
    ...overrides,
  }
}

/** Mounts the admin and account routes with real auth, query and toast providers. */
export function mountConsole(path: string, routes: Record<string, Handler> = {}, method: 'oidc' | 'bearer' = 'oidc') {
  if (method === 'bearer') sessionStorage.setItem('graphnest_token', 'admin')
  const calls = stubFetch(consoleRoutes(routes, method))
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>
          <TooltipProvider>
            <MemoryRouter initialEntries={[path]}>
              <Gate>
                <Routes>
                  <Route path="admin" element={<AdminPage />} />
                  <Route path="admin/:section" element={<AdminPage />} />
                  <Route path="account" element={<AccountPage />} />
                </Routes>
              </Gate>
            </MemoryRouter>
          </TooltipProvider>
          <Toaster />
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>,
  )
  return { ...view, calls, queryClient }
}
