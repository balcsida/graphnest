import { Suspense, lazy } from 'react'
import { useParams } from 'react-router'
import { NotFound } from '@/components/NotFound'
import { Skeleton } from '@/components/ui/skeleton'
import CompareView from './CompareView'
import ComponentsView from './ComponentsView'
import OverviewView from './OverviewView'
import RepositoriesView from './RepositoriesView'

// Recharts and React Flow stay out of the main and the other supply-chain chunks.
const LicensesView = lazy(() => import('./licenses/LicensesView'))
const GraphView = lazy(() => import('./graph/GraphView'))

/** /supply-chain and /supply-chain/:view; the stream and every filter live in the URL search parameters. */
export default function SupplyChainPage() {
  const { view } = useParams()
  switch (view) {
    case undefined:
      return <OverviewView />
    case 'repositories':
      return <RepositoriesView />
    case 'components':
      return <ComponentsView />
    case 'compare':
      return <CompareView />
    case 'licenses':
      return (
        <Suspense fallback={<Skeleton className="h-32 w-full" aria-label="Loading licenses" />}>
          <LicensesView />
        </Suspense>
      )
    case 'graph':
      return (
        <Suspense fallback={<Skeleton className="h-32 w-full" aria-label="Loading dependency graph" />}>
          <GraphView />
        </Suspense>
      )
    default:
      return <NotFound />
  }
}
