import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { SearchStateProvider } from '@/pages/search/state'
import { AuthProvider, useAuth } from '@/lib/auth'

// Test helper file: it exports a mount function and fixtures next to a private component.
// eslint-disable-next-line react-refresh/only-export-components
function Ready({ children }: { children: ReactNode }) {
  return useAuth().status === 'authenticated' ? children : null
}

/** Mounts a search-area page with real auth, query and search-state providers; routes come from stubFetch. */
/** `gated: false` keeps the page mounted after sign-out so a test can observe the reset. */
export function mountPage(page: ReactNode, path = '/', gated = true) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <AuthProvider>
        <MemoryRouter initialEntries={[path]}>
          <SearchStateProvider>
            {gated ? <Ready>{page}</Ready> : page}
          </SearchStateProvider>
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

export const session = { 'GET /v1/auth/session': { body: { method: 'oidc' } } }

export const repositoryFixture = (name: string, id: number, extra: object = {}) => ({
  id,
  github_id: id,
  name,
  branch: 'main',
  desired_sha: '',
  indexed_sha: 'a'.repeat(40),
  web_url: `https://github.com/${name}`,
  status: 'ready',
  error_code: '',
  search_node: '',
  scip_status: 'unknown',
  ...extra,
})
