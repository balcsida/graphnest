import { Bot, Code2, FolderGit2, Gauge, GitFork, KeyRound, Package, Scale, Settings2, ShieldCheck, Users, type LucideIcon } from 'lucide-react'

export interface NavItem {
  title: string
  to: string
  icon: LucideIcon
  /** Hidden while signed in with a bearer token (identity screens are session-only). */
  sessionOnly?: boolean
}

export interface NavGroup {
  id: 'code' | 'supply-chain' | 'administration' | 'account'
  label: string
  items: NavItem[]
}

const adminSection = (title: string, section: string, icon: LucideIcon, sessionOnly = false): NavItem => ({
  title,
  to: `/admin/${section}`,
  icon,
  sessionOnly,
})

/** Sidebar groups in display order. The supply-chain and administration groups are probe-gated in AppSidebar. */
export const navGroups: NavGroup[] = [
  {
    id: 'code',
    label: 'Code',
    items: [
      { title: 'Search', to: '/', icon: Code2 },
      { title: 'Repositories', to: '/repositories', icon: FolderGit2 },
      { title: 'Connect an agent', to: '/connect', icon: Bot },
    ],
  },
  {
    id: 'supply-chain',
    label: 'Supply chain',
    items: [
      { title: 'Overview', to: '/supply-chain', icon: Gauge },
      { title: 'Repositories', to: '/supply-chain/repositories', icon: FolderGit2 },
      { title: 'Components', to: '/supply-chain/components', icon: Package },
      { title: 'Licenses', to: '/supply-chain/licenses', icon: Scale },
      { title: 'Dependency graph', to: '/supply-chain/graph', icon: GitFork },
      { title: 'Compare', to: '/supply-chain/compare', icon: ShieldCheck },
    ],
  },
  {
    id: 'administration',
    label: 'Administration',
    items: [
      { title: 'Overview', to: '/admin', icon: Gauge },
      adminSection('Repositories', 'repositories', FolderGit2),
      adminSection('Jobs', 'jobs', Settings2),
      adminSection('Deliveries', 'deliveries', Settings2),
      adminSection('GitHub', 'github', Settings2),
      adminSection('SCIP', 'scip', Settings2),
      adminSection('Users', 'users', Users, true),
      adminSection('Groups', 'groups', Users, true),
      adminSection('Audit', 'audit', ShieldCheck, true),
    ],
  },
  { id: 'account', label: 'Account', items: [{ title: 'Account', to: '/account', icon: KeyRound }] },
]

const titles = new Map(navGroups.flatMap((group) => group.items.map((item) => [item.to, `${group.label}: ${item.title}`] as const)))

/** Breadcrumb trail for a pathname: the group label, then the item title when the route is a nav entry. */
export function breadcrumbFor(pathname: string): string[] {
  const path = pathname.replace(/\/+$/, '') || '/'
  const known = titles.get(path)
  if (known) return known.split(': ')
  return path
    .split('/')
    .filter(Boolean)
    .map((segment) => decodeURIComponent(segment).replace(/-/g, ' ').replace(/^./, (c) => c.toUpperCase()))
}
