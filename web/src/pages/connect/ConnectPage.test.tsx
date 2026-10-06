import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { mountConsole } from '@/test/admin-harness'

const metadata = 'GET /.well-known/oauth-protected-resource'
const panel = () => screen.getByRole('tabpanel')

describe('connect page', () => {
  it('uses this origin and a token when OAuth metadata is missing', async () => {
    const user = userEvent.setup()
    mountConsole('/connect')
    expect(await screen.findByRole('heading', { name: 'Connect an agent' })).toBeInTheDocument()
    expect(screen.getByText(`${window.location.origin}/mcp`)).toBeInTheDocument()
    expect(screen.queryByLabelText('Sign in with OAuth instead of a token')).not.toBeInTheDocument()
    expect(panel()).toHaveTextContent("--header 'Authorization: Bearer ${GRAPHNEST_TOKEN}'")
    await user.click(screen.getByRole('tab', { name: 'Codex' }))
    expect(panel()).toHaveTextContent('--bearer-token-env-var GRAPHNEST_TOKEN')
  })

  it('offers OAuth when the server advertises it', async () => {
    const user = userEvent.setup()
    mountConsole('/connect', { [metadata]: { body: { resource: 'https://graphnest.example.com/mcp' } } })
    const toggle = await screen.findByLabelText('Sign in with OAuth instead of a token')
    expect(screen.getByText('https://graphnest.example.com/mcp')).toBeInTheDocument()
    await user.click(toggle)
    expect(panel()).not.toHaveTextContent('--header')
    await user.click(screen.getByRole('tab', { name: 'Codex' }))
    expect(panel()).toHaveTextContent('codex mcp login graphnest')
    await user.click(screen.getByRole('tab', { name: 'Antigravity CLI' }))
    expect(panel()).toHaveTextContent("Antigravity CLI doesn't send OAuth tokens")
    expect(panel()).toHaveTextContent('agy mcp add --type http')
  })

  it('copies a snippet', async () => {
    const user = userEvent.setup()
    mountConsole('/connect')
    const writeText = vi.spyOn(navigator.clipboard, 'writeText')
    await screen.findByRole('heading', { name: 'Connect an agent' })
    await user.click(within(panel()).getAllByRole('button', { name: 'Copy Terminal' })[0])
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining('claude mcp add --transport http'))
    expect(await screen.findByText('Copied')).toBeInTheDocument()
  })
})
