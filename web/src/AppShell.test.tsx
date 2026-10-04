import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AppRoutes } from '@/App'
import { TooltipProvider } from '@/components/ui/tooltip'
import { AuthProvider } from '@/lib/auth'
import { ThemeProvider } from '@/lib/theme'
import { authConfig, stubFetch, unauthenticated } from '@/test/fetch'

function mount(path: string) {
  render(
    <ThemeProvider>
      <QueryClientProvider client={new QueryClient()}>
        <AuthProvider>
          <TooltipProvider>
            <MemoryRouter initialEntries={[path]}>
              <AppRoutes />
            </MemoryRouter>
          </TooltipProvider>
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>,
  )
}

const signedIn = {
  'GET /v1/auth/config': { body: authConfig },
  'GET /v1/auth/session': { body: { method: 'oidc' } },
}

describe('app shell', () => {
  it('renders groups for an administrator with supply chain enabled', async () => {
    stubFetch({
      ...signedIn,
      'GET /v1/supply-chain/overview': { body: {} },
      'GET /v1/admin/overview': { body: {} },
    })
    mount('/repositories')
    await waitFor(() => expect(document.querySelector('[data-slot="card-title"]')).toHaveTextContent('Repositories'))
    expect(await screen.findByText('Supply chain')).toBeInTheDocument()
    expect(await screen.findByText('Administration')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Search' })).toHaveAttribute('href', '/')
    expect(screen.getByRole('link', { name: 'Users' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /sign out/i })).toBeInTheDocument()
  })

  it('hides supply chain on 404 and administration when the probe fails', async () => {
    stubFetch(signedIn)
    mount('/')
    expect(await screen.findByRole('link', { name: 'Account' })).toBeInTheDocument()
    expect(screen.queryByText('Supply chain')).not.toBeInTheDocument()
    expect(screen.queryByText('Administration')).not.toBeInTheDocument()
  })

  it('renders an unknown route as not found inside the shell', async () => {
    stubFetch(signedIn)
    mount('/nope')
    expect(await screen.findByText('Page not found')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /sign out/i })).toBeInTheDocument()
  })

  it('shows the gate instead of the shell when unauthenticated', async () => {
    stubFetch({ 'GET /v1/auth/config': { body: authConfig }, 'GET /v1/auth/session': unauthenticated })
    mount('/')
    expect(await screen.findByLabelText('API token')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /sign out/i })).not.toBeInTheDocument()
  })
})
