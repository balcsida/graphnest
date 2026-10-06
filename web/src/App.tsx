import { lazy } from 'react'
import { BrowserRouter, Outlet, Route, Routes } from 'react-router'
import { AppShell } from '@/components/AppShell'
import { NotFound } from '@/components/NotFound'
import { TokenGate } from '@/components/auth/TokenGate'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/lib/auth'

const SearchPage = lazy(() => import('@/pages/search/SearchPage'))
const RepositoriesPage = lazy(() => import('@/pages/repositories/RepositoriesPage'))
const AdminPage = lazy(() => import('@/pages/admin/AdminPage'))
const AccountPage = lazy(() => import('@/pages/account/AccountPage'))
const ConnectPage = lazy(() => import('@/pages/connect/ConnectPage'))
const SupplyChainPage = lazy(() => import('@/pages/supply-chain/SupplyChainPage'))

function RequireAuth() {
  const { status } = useAuth()
  if (status === 'loading') return <Skeleton className="m-4 h-32" aria-label="Loading" />
  if (status === 'gate') return <TokenGate />
  return <Outlet />
}

export function AppRoutes() {
  return (
    <Routes>
      <Route element={<RequireAuth />}>
        <Route element={<AppShell />}>
          <Route index element={<SearchPage />} />
          <Route path="repositories" element={<RepositoriesPage />} />
          <Route path="admin" element={<AdminPage />} />
          <Route path="admin/:section" element={<AdminPage />} />
          <Route path="connect" element={<ConnectPage />} />
          <Route path="account" element={<AccountPage />} />
          <Route path="supply-chain" element={<SupplyChainPage />} />
          <Route path="supply-chain/:view" element={<SupplyChainPage />} />
          <Route path="*" element={<NotFound />} />
        </Route>
      </Route>
    </Routes>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <AppRoutes />
    </BrowserRouter>
  )
}
