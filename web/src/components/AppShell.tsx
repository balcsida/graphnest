import { Suspense } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, NavLink, Outlet, useLocation } from 'react-router'
import { LogOut } from 'lucide-react'
import { toast } from 'sonner'
import { getAdminOverview } from '@/api/admin'
import { getSupplyChainOverview } from '@/api/supply-chain'
import { ThemeToggle } from '@/components/ThemeToggle'
import { breadcrumbFor, navGroups } from '@/components/nav'
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from '@/components/ui/breadcrumb'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
} from '@/components/ui/sidebar'
import { ApiError } from '@/lib/api'
import { useAuth } from '@/lib/auth'

function useNavVisibility() {
  const supplyChain = useQuery({
    queryKey: ['nav', 'supply-chain'],
    queryFn: ({ signal }) => getSupplyChainOverview(signal),
    retry: false,
    staleTime: Infinity,
  })
  const admin = useQuery({
    queryKey: ['nav', 'admin'],
    queryFn: ({ signal }) => getAdminOverview(signal),
    retry: false,
    staleTime: Infinity,
  })
  return {
    // Shown unless the feature is off (404); other failures keep it visible so the page can explain them.
    'supply-chain': supplyChain.isSuccess || (supplyChain.isError && !(supplyChain.error instanceof ApiError && supplyChain.error.status === 404)),
    administration: admin.isSuccess,
    code: true,
    account: true,
  }
}

function AppSidebar() {
  const { method } = useAuth()
  const { pathname } = useLocation()
  const visible = useNavVisibility()
  const path = pathname.replace(/\/+$/, '') || '/'
  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <Link to="/" className="px-2 py-1 text-lg font-semibold">GraphNest</Link>
      </SidebarHeader>
      <SidebarContent>
        {navGroups
          .filter((group) => visible[group.id])
          .map((group) => (
            <SidebarGroup key={group.id}>
              <SidebarGroupLabel>{group.label}</SidebarGroupLabel>
              <SidebarGroupContent>
                <SidebarMenu>
                  {group.items
                    .filter((item) => !(item.sessionOnly && method === 'bearer'))
                    .map((item) => (
                      <SidebarMenuItem key={item.to}>
                        <SidebarMenuButton asChild isActive={path === item.to} tooltip={item.title}>
                          <NavLink to={item.to} end>
                            <item.icon />
                            <span>{item.title}</span>
                          </NavLink>
                        </SidebarMenuButton>
                      </SidebarMenuItem>
                    ))}
                </SidebarMenu>
              </SidebarGroupContent>
            </SidebarGroup>
          ))}
      </SidebarContent>
      <SidebarRail />
    </Sidebar>
  )
}

function Header() {
  const { signOut } = useAuth()
  const { pathname } = useLocation()
  const trail = breadcrumbFor(pathname)

  async function handleSignOut() {
    try {
      await signOut()
    } catch (failure) {
      toast.error(failure instanceof Error ? failure.message : 'Unable to sign out.')
    }
  }

  return (
    <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
      <SidebarTrigger />
      <Separator orientation="vertical" className="mr-2 h-4" />
      <Breadcrumb className="flex-1">
        <BreadcrumbList>
          {trail.map((label, index) => (
            <BreadcrumbItem key={`${index}-${label}`}>
              {index > 0 && <BreadcrumbSeparator />}
              {index === trail.length - 1 ? <BreadcrumbPage>{label}</BreadcrumbPage> : <span>{label}</span>}
            </BreadcrumbItem>
          ))}
          {trail.length === 0 && (
            <BreadcrumbItem>
              <BreadcrumbPage>Search</BreadcrumbPage>
            </BreadcrumbItem>
          )}
        </BreadcrumbList>
      </Breadcrumb>
      <ThemeToggle />
      <Button variant="ghost" size="sm" onClick={handleSignOut}>
        <LogOut /> Sign out
      </Button>
    </header>
  )
}

export function AppShell() {
  return (
    <SidebarProvider>
      <AppSidebar />
      <SidebarInset>
        <Header />
        <div className="flex-1 p-4">
          <Suspense fallback={<Skeleton className="h-32 w-full" aria-label="Loading page" />}>
            <Outlet />
          </Suspense>
        </div>
      </SidebarInset>
    </SidebarProvider>
  )
}
