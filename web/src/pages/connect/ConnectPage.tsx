import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Copy, ExternalLink } from 'lucide-react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { PageHeader } from '@/pages/admin/shared'
import { clientsFor, type Step } from './clients'

const metadataPath = '/.well-known/oauth-protected-resource'

/** The canonical MCP URL when the server is an OAuth authorization server, otherwise null. */
async function fetchOAuthResource(): Promise<string | null> {
  const response = await fetch(metadataPath, { cache: 'no-store' })
  if (!response.ok) return null
  const body: unknown = await response.json()
  const resource = (body as { resource?: unknown } | null)?.resource
  return typeof resource === 'string' ? resource : null
}

function CodeBlock({ label, code }: { label?: string; code: string }) {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code)
      toast('Copied')
    } catch {
      toast.error('Could not copy to the clipboard.')
    }
  }
  return (
    <div className="grid grid-cols-1 gap-1">
      {label && <span className="text-xs text-muted-foreground">{label}</span>}
      <div className="flex min-w-0 items-start gap-2 rounded-md border bg-muted/40 p-2">
        <pre className="min-w-0 flex-1 overflow-x-auto font-mono text-[13px]">
          <code>{code}</code>
        </pre>
        <Button type="button" variant="ghost" size="icon-xs" aria-label={label ? `Copy ${label}` : 'Copy'} onClick={copy}>
          <Copy />
        </Button>
      </div>
    </div>
  )
}

function Steps({ steps }: { steps: Step[] }) {
  return (
    <ol className="grid grid-cols-1 list-decimal gap-3 pl-5 text-sm">
      {steps.map((step, index) => (
        <li key={index} className="min-w-0 pl-1">
          <div className="grid grid-cols-1 gap-2">
            <span>{step.text}</span>
            {step.code && <CodeBlock label={step.label} code={step.code} />}
          </div>
        </li>
      ))}
    </ol>
  )
}

export default function ConnectPage() {
  const [signIn, setSignIn] = useState(false)
  const metadata = useQuery({ queryKey: ['oauth-protected-resource'], queryFn: fetchOAuthResource })
  const resource = metadata.data ?? null
  const url = resource ?? `${window.location.origin}/mcp`
  const oauth = resource !== null && signIn
  const clients = clientsFor({ url, oauth })

  return (
    <div className="grid grid-cols-1 gap-6">
      <PageHeader
        title="Connect an agent"
        subtitle="Agents connect over MCP (Model Context Protocol) and can then search code and explore the code graph with your access."
      />

      <section className="grid grid-cols-1 gap-2">
        <h2 className="text-sm font-semibold">Server address</h2>
        <CodeBlock code={url} />
      </section>

      <section className="grid grid-cols-1 gap-3">
        <h2 className="text-sm font-semibold">Authentication</h2>
        {resource !== null && (
          <div className="flex items-center gap-2">
            <Switch id="connect-oauth" checked={signIn} onCheckedChange={setSignIn} />
            <Label htmlFor="connect-oauth">Sign in with OAuth instead of a token</Label>
          </div>
        )}
        {oauth ? (
          <p className="text-sm">Your agent opens a browser to sign in to GraphNest and you approve the request. No token needed.</p>
        ) : (
          <>
            <Steps
              steps={[
                {
                  text: (
                    <>
                      Create an API token on your{' '}
                      <Link to="/account" className="underline underline-offset-4">
                        Account page
                      </Link>
                      .
                    </>
                  ),
                },
                {
                  text: (
                    <>
                      Make it available as <code>GRAPHNEST_TOKEN</code>, for example in your shell profile.
                    </>
                  ),
                  label: 'Shell profile',
                  code: 'export GRAPHNEST_TOKEN=<your token>',
                },
              ]}
            />
            <p className="text-xs text-muted-foreground">Most clients below read the token from this variable, so it doesn&apos;t end up in their config files.</p>
          </>
        )}
      </section>

      <Tabs defaultValue={clients[0].id}>
        <TabsList aria-label="Agent clients">
          {clients.map((client) => (
            <TabsTrigger key={client.id} value={client.id}>
              {client.name}
            </TabsTrigger>
          ))}
        </TabsList>
        {clients.map((client) => (
          <TabsContent key={client.id} value={client.id} className="grid grid-cols-1 gap-4 pt-2">
            <Steps steps={[...client.steps, client.check]} />
            {client.notes.map((note, index) => (
              <p key={index} className="text-xs text-muted-foreground">
                {note}
              </p>
            ))}
            <a
              href={client.docs}
              target="_blank"
              rel="noreferrer"
              className="inline-flex w-fit items-center gap-1 text-sm underline underline-offset-4"
            >
              Full documentation
              <ExternalLink className="size-3.5" aria-hidden />
            </a>
          </TabsContent>
        ))}
      </Tabs>
    </div>
  )
}
