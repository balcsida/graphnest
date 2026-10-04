import { useAuth } from '@/lib/auth'
import { PageHeader } from '@/pages/admin/shared'
import { useAdminOverview } from '@/pages/admin/use-admin-overview'
import { GrantsSection } from './GrantsSection'
import { TokensSection } from './TokensSection'

export default function AccountPage() {
  const { method } = useAuth()
  const administrator = useAdminOverview().isSuccess
  return (
    <div className="grid gap-6">
      <PageHeader title="Account" subtitle="API tokens and connected MCP clients" />
      <TokensSection delegation={administrator && method !== 'bearer'} />
      <GrantsSection />
    </div>
  )
}
