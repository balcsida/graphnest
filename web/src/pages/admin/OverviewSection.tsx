import { useQuery } from '@tanstack/react-query'
import type { AdminOverview } from '@/api/types'
import { cn } from '@/lib/utils'
import { queueDepth, repositoryTotal } from './format'
import { Panel, StatCard } from './shared'

type Health = 'ok' | 'down' | 'unreachable'

function useHealth(path: '/healthz' | '/readyz') {
  return useQuery({
    queryKey: ['admin', 'health', path],
    queryFn: async ({ signal }): Promise<Health> => {
      try {
        const response = await fetch(path, { cache: 'no-store', signal })
        return response.ok ? 'ok' : 'down'
      } catch (failure) {
        if (signal.aborted) throw failure
        return 'unreachable'
      }
    },
    refetchInterval: 30_000,
    retry: false,
  })
}

function HealthLine({ path, name }: { path: '/healthz' | '/readyz'; name: 'Health' | 'Readiness' }) {
  const health = useHealth(path).data
  const text = health === 'ok' ? `${name} OK` : health === 'unreachable' ? 'Unavailable' : `${name} unavailable`
  return (
    <p className="flex items-center gap-2 text-sm">
      <span aria-hidden="true" className={cn('size-2.5 rounded-full', health === 'ok' ? 'bg-emerald-600' : 'bg-destructive')} />
      <span>{text}</span>
    </p>
  )
}

export function OverviewSection({ overview }: { overview: AdminOverview }) {
  const depth = queueDepth(overview)
  const deliveries = Object.values(overview.deliveries ?? {}).reduce((sum, count) => sum + count, 0)
  return (
    <div className="grid gap-4">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Repositories" value={repositoryTotal(overview)} />
        <StatCard label="Queue depth" value={depth} tone={depth > 0 ? 'warn' : undefined} />
        <StatCard label="SCIP indexes" value={overview.scip_uploads || 0} />
        <StatCard label="Installations" value={overview.installations || 0} />
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title="Dependency health">
          <p className="text-muted-foreground">
            Process liveness and durable readiness are checked directly. Status updates every 30 seconds.
          </p>
          <HealthLine path="/healthz" name="Health" />
          <HealthLine path="/readyz" name="Readiness" />
        </Panel>
        <Panel title="Recent activity">
          <p>Webhook deliveries: {deliveries}</p>
          <p>Dependencies: {overview.dependencies || 0}</p>
        </Panel>
      </div>
    </div>
  )
}
