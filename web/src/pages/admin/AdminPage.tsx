import type { ReactNode } from 'react'
import { Navigate, useParams } from 'react-router'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Skeleton } from '@/components/ui/skeleton'
import { NotFound } from '@/components/NotFound'
import { ApiError } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { AuditSection } from './AuditSection'
import { DeliveriesSection } from './DeliveriesSection'
import { GitHubSection } from './GitHubSection'
import { GroupsSection } from './GroupsSection'
import { JobsSection } from './JobsSection'
import { OverviewSection } from './OverviewSection'
import { RepositoriesSection } from './RepositoriesSection'
import { ScipSection } from './ScipSection'
import { UsersSection } from './UsersSection'
import { ErrorNotice, PageHeader } from './shared'
import { useAdminOverview } from './use-admin-overview'

interface Screen {
  title: string
  subtitle: string
  /** Identity screens exist only for browser sessions. */
  sessionOnly?: boolean
}

const screens: Record<string, Screen> = {
  overview: { title: 'Overview', subtitle: 'system health at a glance' },
  repositories: { title: 'Repositories', subtitle: 'index state reconciled from GitHub' },
  jobs: { title: 'Index queue', subtitle: 'durable default-branch indexing' },
  deliveries: { title: 'Webhook deliveries', subtitle: 'verified GitHub HMAC intake' },
  github: { title: 'GitHub App', subtitle: 'sanitized configuration and installations' },
  scip: { title: 'SCIP indexes', subtitle: 'pre-generated code-navigation data' },
  users: { title: 'Users', subtitle: 'effective and direct access', sessionOnly: true },
  groups: { title: 'Groups', subtitle: 'access granted to OIDC groups', sessionOnly: true },
  audit: { title: 'Audit events', subtitle: 'bounded security activity', sessionOnly: true },
}

const lockMessages: Record<number, string> = {
  401: 'Token required or expired.',
  403: 'This account is not an administrator.',
  404: 'Administrator API is unavailable in static mode.',
}

/** A 401, 403 or 404 from the overview probe means "not an administrator"; any other failure is an ordinary error. */
const lockStatus = (error: unknown) => (error instanceof ApiError && error.status in lockMessages ? error.status : null)

export default function AdminPage() {
  const { section = 'overview' } = useParams()
  const { method } = useAuth()
  const probe = useAdminOverview(true)
  const screen = Object.hasOwn(screens, section) ? screens[section] : undefined

  if (!screen) return <NotFound />
  if (screen.sessionOnly && method === 'bearer') return <Navigate to="/admin" replace />

  const locked = lockStatus(probe.error)
  let body: ReactNode
  if (locked !== null) {
    body = (
      <Alert variant="destructive" aria-live="assertive">
        <AlertDescription>{lockMessages[locked]}</AlertDescription>
      </Alert>
    )
  } else if (probe.data === undefined) {
    body = probe.isError ? (
      <ErrorNotice error={probe.error} onRetry={() => void probe.refetch()} />
    ) : (
      <Skeleton className="h-32 w-full" aria-label="Loading administration" />
    )
  } else {
    body = (
      <>
        {probe.isError && <ErrorNotice error={probe.error} />}
        {section === 'overview' && <OverviewSection overview={probe.data} />}
        {section === 'repositories' && <RepositoriesSection />}
        {section === 'jobs' && <JobsSection />}
        {section === 'deliveries' && <DeliveriesSection />}
        {section === 'github' && <GitHubSection />}
        {section === 'scip' && <ScipSection />}
        {section === 'users' && <UsersSection />}
        {section === 'groups' && <GroupsSection />}
        {section === 'audit' && <AuditSection />}
      </>
    )
  }

  return (
    <div className="grid gap-4">
      <PageHeader title={screen.title} subtitle={screen.subtitle} />
      {body}
    </div>
  )
}
