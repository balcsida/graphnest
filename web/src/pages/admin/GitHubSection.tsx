import { useQuery } from '@tanstack/react-query'
import { getAdminGitHub, reconcileGitHub } from '@/api/admin'
import { Button } from '@/components/ui/button'
import { bounded } from './format'
import { Panel, PartialNotice, QueryBoundary, StatCard, mono } from './shared'
import { useAdminActions } from './use-admin-actions'

export function GitHubSection() {
  const query = useQuery({ queryKey: ['admin', 'github'], queryFn: ({ signal }) => getAdminGitHub(signal), refetchInterval: 30_000, retry: false })
  const { confirmed, dialog } = useAdminActions()
  return (
    <QueryBoundary query={query} label="GitHub App">
      {(github) => {
        const installations = bounded(github.installations)
        const config: [string, string][] = [
          ['Web URL', github.web_url],
          ['API URL', github.api_url],
          ['Upload URL', github.upload_url],
          ['Git URL', github.git_url],
          ['API version', github.api_version],
          ['Custom CA', github.ca_configured ? 'Configured' : 'Default trust'],
        ]
        return (
          <div className="grid gap-4">
            {github.truncated && <PartialNotice names={['installations']} />}
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <StatCard label="App ID" value={github.app_id} />
              <StatCard label="Installations" value={installations.length} />
              <StatCard label="Private key" value={github.private_key_configured ? 'Configured' : 'Missing'} />
              <StatCard label="Webhook secret" value={github.webhook_secret_configured ? 'Configured' : 'Missing'} />
            </div>
            <div className="grid gap-4 lg:grid-cols-2">
              <Panel title="App configuration">
                <dl className="grid gap-2">
                  {config.map(([label, value]) => (
                    <div key={label} className="grid gap-0.5">
                      <dt className="text-muted-foreground">{label}</dt>
                      <dd className={mono}>{value}</dd>
                    </div>
                  ))}
                </dl>
              </Panel>
              <Panel
                title="Installations"
                action={
                  <Button type="button" variant="outline" size="sm" onClick={() => void confirmed('Reconcile repositories from GitHub now?', reconcileGitHub, 'Reconciliation completed.')}>
                    Run now
                  </Button>
                }
              >
                {installations.length === 0 && <p className="text-muted-foreground">No installations are visible to this administrator.</p>}
                <ul className="grid gap-2">
                  {installations.map((installation) => (
                    <li key={installation.github_id} className={mono}>
                      {installation.account_login} · {installation.account_type} · {installation.status} · #{installation.github_id}
                    </li>
                  ))}
                </ul>
              </Panel>
            </div>
            {dialog}
          </div>
        )
      }}
    </QueryBoundary>
  )
}
