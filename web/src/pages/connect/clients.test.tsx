import { describe, expect, it } from 'vitest'
import { clientsFor } from './clients'

const url = 'https://graphnest.example.com/mcp'
const snippets = (id: string, oauth: boolean) =>
  clientsFor({ url, oauth })
    .find((client) => client.id === id)!
    .steps.map((step) => step.code ?? '')
    .join('\n')

describe('clientsFor', () => {
  it('covers the eight clients', () => {
    expect(clientsFor({ url, oauth: false }).map((client) => client.name)).toEqual([
      'Claude Code', 'Codex', 'OpenCode', 'Pi', 'Cursor', 'VS Code', 'Antigravity CLI', 'Claude Desktop',
    ])
  })

  it('keeps variable references literal in token mode', () => {
    expect(snippets('cursor', false)).toContain('${env:GRAPHNEST_TOKEN}')
    expect(snippets('vscode', false)).toContain('${input:graphnest-token}')
    expect(snippets('claude-code', false)).toContain("--header 'Authorization: Bearer ${GRAPHNEST_TOKEN}'")
  })

  it('points the Claude Desktop proxy at the origin', () => {
    const code = snippets('claude-desktop', false)
    expect(code).toContain('"GRAPHNEST_SERVER_URL": "https://graphnest.example.com"')
    expect(code).toContain('"GRAPHNEST_TOKEN": "<your token>"')
  })
})
