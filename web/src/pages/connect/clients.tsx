import type { ReactNode } from 'react'
import { Link } from 'react-router'

export interface Step {
  text: ReactNode
  /** Label above the snippet, such as "Terminal" or a config file path. */
  label?: string
  code?: string
}

export interface Client {
  id: string
  name: string
  docs: string
  steps: Step[]
  check: Step
  notes: ReactNode[]
}

export interface ClientOptions {
  url: string
  oauth: boolean
}

const accountLink = <Link to="/account" className="underline underline-offset-4">Account page</Link>
const createToken: Step = { text: <>Create an API token on your {accountLink} and export it as <code>GRAPHNEST_TOKEN</code>.</> }

const json = (value: unknown) => JSON.stringify(value, null, 2)

export function clientsFor({ url, oauth }: ClientOptions): Client[] {
  const server = url.replace(/\/mcp$/, '')
  const claudeCode = oauth
    ? [
        { text: 'Add the server.', label: 'Terminal', code: `claude mcp add --transport http --scope user graphnest ${url}` },
        { text: <>Start Claude Code, run <code>/mcp</code>, select graphnest and choose Authenticate.</> },
      ]
    : [
        {
          text: 'Add the server.',
          label: 'Terminal',
          code: `claude mcp add --transport http --scope user graphnest ${url} \\\n  --header 'Authorization: Bearer \${GRAPHNEST_TOKEN}'`,
        },
      ]
  const codex = oauth
    ? [
        { text: 'Add the server.', label: 'Terminal', code: `codex mcp add graphnest --url ${url}` },
        { text: 'Sign in.', label: 'Terminal', code: 'codex mcp login graphnest' },
      ]
    : [{ text: 'Add the server.', label: 'Terminal', code: `codex mcp add graphnest --url ${url} --bearer-token-env-var GRAPHNEST_TOKEN` }]
  const opencodeServer = oauth
    ? { type: 'remote', url }
    : { type: 'remote', url, oauth: false, headers: { Authorization: 'Bearer {env:GRAPHNEST_TOKEN}' } }
  const opencode: Step[] = [
    {
      text: 'Add the server to your config, merging with existing content.',
      label: '~/.config/opencode/opencode.json',
      code: json({ $schema: 'https://opencode.ai/config.json', mcp: { graphnest: opencodeServer } }),
    },
    ...(oauth ? [{ text: 'Sign in.', label: 'Terminal', code: 'opencode mcp auth graphnest' }] : []),
  ]
  const pi = oauth
    ? [
        { text: 'Add the server.', label: 'Terminal', code: `pi mcp add graphnest --url ${url}` },
        { text: 'Sign in.', label: 'Terminal', code: 'pi mcp login graphnest' },
      ]
    : [{ text: 'Add the server.', label: 'Terminal', code: `pi mcp add graphnest --url ${url} --bearer-token-env-var GRAPHNEST_TOKEN` }]
  const cursor: Step[] = [
    {
      text: 'Add the server to your config.',
      label: '~/.cursor/mcp.json',
      code: json({ mcpServers: { graphnest: oauth ? { url } : { url, headers: { Authorization: 'Bearer ${env:GRAPHNEST_TOKEN}' } } } }),
    },
    ...(oauth ? [{ text: 'Open the MCP section of Cursor Settings and sign in to graphnest when asked.' }] : []),
  ]
  const vscode: Step[] = oauth
    ? [
        {
          text: <>Run &quot;MCP: Open User Configuration&quot; from the Command Palette and add the server.</>,
          code: json({ servers: { graphnest: { type: 'http', url } } }),
        },
        { text: <>Start graphnest from &quot;MCP: List Servers&quot; and sign in when VS Code asks.</> },
      ]
    : [
        {
          text: <>Run &quot;MCP: Open User Configuration&quot; from the Command Palette and add the server.</>,
          code: json({
            inputs: [{ type: 'promptString', id: 'graphnest-token', description: 'GraphNest API token', password: true }],
            servers: { graphnest: { type: 'http', url, headers: { Authorization: 'Bearer ${input:graphnest-token}' } } },
          }),
        },
      ]
  const antigravityCommand: Step = {
    text: 'Add the server.',
    label: 'Terminal',
    code: `agy mcp add --type http \\\n  --header "Authorization: Bearer $GRAPHNEST_TOKEN" graphnest ${url}`,
  }
  const antigravity: Step[] = oauth ? [createToken, antigravityCommand] : [antigravityCommand]
  const proxyInstall =
    'go install github.com/balcsida/graphnest/cmd/graphnest-mcp@latest && echo "$(go env GOPATH)/bin/graphnest-mcp"'

  return [
    {
      id: 'claude-code',
      name: 'Claude Code',
      docs: 'https://code.claude.com/docs/en/mcp',
      steps: claudeCode,
      check: { text: 'Check that graphnest is connected.', label: 'Terminal', code: 'claude mcp get graphnest' },
      notes: oauth ? [] : [<>The single quotes keep <code>{'${GRAPHNEST_TOKEN}'}</code> as written; Claude Code reads the variable whenever it connects.</>],
    },
    {
      id: 'codex',
      name: 'Codex',
      docs: 'https://developers.openai.com/codex/mcp',
      steps: codex,
      check: { text: 'Check that graphnest is connected.', label: 'Terminal', code: 'codex mcp list' },
      notes: oauth ? [] : ['Codex reads the variable from the environment it starts in; the CLI, IDE extension and app share this setting.'],
    },
    {
      id: 'opencode',
      name: 'OpenCode',
      docs: 'https://opencode.ai/docs/mcp-servers/',
      steps: opencode,
      check: { text: 'Check that graphnest is connected.', label: 'Terminal', code: 'opencode mcp list' },
      notes: oauth ? [] : [<><code>&quot;oauth&quot;: false</code> stops OpenCode from starting its own sign-in.</>],
    },
    {
      id: 'pi',
      name: 'Pi',
      docs: 'https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md',
      steps: pi,
      check: { text: 'Check that graphnest is connected.', label: 'Terminal', code: 'pi mcp list' },
      notes: [<>Pi 1.0 and later have MCP built in; in a session that&apos;s already running, run <code>/reload</code>.</>],
    },
    {
      id: 'cursor',
      name: 'Cursor',
      docs: 'https://cursor.com/docs/context/mcp',
      steps: cursor,
      check: { text: "Check that the MCP section of Cursor Settings lists graphnest's tools." },
      notes: oauth ? [] : ['Cursor reads the variable from its own environment, so start Cursor from a shell where it is set.'],
    },
    {
      id: 'vscode',
      name: 'VS Code',
      docs: 'https://code.visualstudio.com/docs/agent-customization/mcp-servers',
      steps: vscode,
      check: { text: <>Check that &quot;MCP: List Servers&quot; shows graphnest as running.</> },
      notes: oauth ? [] : ['VS Code asks for the token the first time the server starts and keeps it in its secret storage.'],
    },
    {
      id: 'antigravity-cli',
      name: 'Antigravity CLI',
      docs: 'https://antigravity.google/docs/cli/mcp/',
      steps: antigravity,
      check: { text: <>Run <code>/mcp</code> in Antigravity CLI and check that graphnest is active.</> },
      notes: [
        ...(oauth ? [<>Antigravity CLI doesn&apos;t send OAuth tokens to remote MCP servers yet, so use a token.</>] : []),
        <>The double quotes let your shell fill in the token now; Antigravity CLI then keeps it in <code>~/.gemini/config/mcp_config.json</code>.</>,
      ],
    },
    {
      id: 'claude-desktop',
      name: 'Claude Desktop',
      docs: 'https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp',
      steps: [
        {
          text: <><strong>If this server is reachable from the internet:</strong> add a custom connector in Claude with this URL.</>,
          code: url,
        },
        oauth
          ? { text: 'Choose to sign in, then approve the request on GraphNest\'s consent page.' }
          : { text: <>Give the connector your API token as a fixed credential: header <code>Authorization</code>, value <code>Bearer &lt;your token&gt;</code>.</> },
        { text: <><strong>Otherwise, run the proxy on your computer:</strong> install it (needs Go). This prints the path to use.</>, label: 'Terminal', code: proxyInstall },
        {
          text: <>Add the proxy to <code>claude_desktop_config.json</code> (Settings → Developer → Edit Config) and restart Claude.</>,
          label: 'claude_desktop_config.json',
          code: json({
            mcpServers: {
              graphnest: { command: '<path printed above>', env: { GRAPHNEST_SERVER_URL: server, GRAPHNEST_TOKEN: '<your token>' } },
            },
          }),
        },
      ],
      check: { text: 'Check that Claude lists graphnest as connected.' },
      notes: [
        "Claude connects to custom connectors from Anthropic's cloud, not from your computer.",
        "Claude Desktop doesn't expand environment variables here, so the token is stored in this file; the proxy always uses a token, even when sign-in is available.",
      ],
    },
  ]
}
