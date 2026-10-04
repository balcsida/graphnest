import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AppRoutes } from '@/App'
import { TooltipProvider } from '@/components/ui/tooltip'
import { AuthProvider } from '@/lib/auth'
import { ThemeProvider } from '@/lib/theme'
import { consoleRoutes } from '@/test/admin-harness'
import { stubFetch } from '@/test/fetch'

function mountShell(path: string) {
  stubFetch(consoleRoutes())
  render(
    <ThemeProvider>
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
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

describe('app shell', () => {
  it('titles the page from the breadcrumb', async () => {
    mountShell('/supply-chain/licenses')
    await waitFor(() => expect(document.title).toBe('Supply chain · Licenses · GraphNest'))
  })

  it('titles the search page GraphNest', async () => {
    mountShell('/')
    await screen.findByRole('link', { name: 'Skip to content' })
    expect(document.title).toBe('GraphNest')
  })

  it('offers a skip link that is the first focusable element and targets the main region', async () => {
    const user = userEvent.setup()
    mountShell('/account')
    const link = await screen.findByRole('link', { name: 'Skip to content' })
    await user.tab()
    expect(link).toHaveFocus()
    expect(link).toHaveAttribute('href', '#main-content')
    const main = screen.getByRole('main')
    expect(main).toHaveAttribute('id', 'main-content')
    expect(main).toHaveAttribute('tabindex', '-1')
  })

  it('names the sidebar counts with hidden text', async () => {
    mountShell('/account')
    expect(await screen.findByText('repositories', { exact: false, selector: '[data-sidebar="menu-badge"] span' })).toHaveClass('sr-only')
    expect(screen.getByText(/jobs queued or running/)).toHaveClass('sr-only')
  })
})
