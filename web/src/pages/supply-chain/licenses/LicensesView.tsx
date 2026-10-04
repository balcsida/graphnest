import { useMemo, useState } from 'react'
import { useQueries, useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router'
import { Bar, BarChart, CartesianGrid, Cell, Pie, PieChart, XAxis, YAxis } from 'recharts'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ChartContainer, ChartLegend, ChartLegendContent, ChartTooltip, ChartTooltipContent, type ChartConfig } from '@/components/ui/chart'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { getSupplyChainFacets, getSupplyChainOverview } from '@/api/supply-chain'
import type { SupplyChainFacets, SupplyChainOverview } from '@/api/types'
import { FAMILY_LABEL, LICENSE_FAMILIES, type LicenseFamily } from '@/lib/license-family'
import { ErrorNotice, Panel } from '@/pages/admin/shared'
import { ChoiceSelect, DisabledModule, MetricCard, StateMessage, StreamSelect, ViewHeader } from '../shared'
import { denominatorText, isDisabled, text, when } from '../format'
import { useViewParams } from '../params'
import { useSupplyChainRepositories } from '../use-repositories'
import {
  REPOSITORY_FACET_LIMIT,
  assessmentTotals,
  repositoryLicenseMix,
  statusSlices,
  sumFacets,
  topLicenses,
  type RepositoryFacets,
  type RepositoryMix,
} from './aggregate'
import { ChartCard } from './ChartCard'
import { ScopePicker } from './ScopePicker'

const TOP_N_CHOICES = [5, 10, 20, 40].map((value) => ({ value: String(value), label: `Top ${value} repositories` }))
const DEFAULT_TOP_N = 10

const chartColor = (index: number) => `var(--chart-${(index % 5) + 1})`

/** Statuses with a clear good/warn/bad reading reuse the licence palette; others cycle the chart colours. */
const STATUS_COLOR: Record<string, string> = {
  resolved: 'var(--license-permissive)',
  conflict: 'var(--license-strong_copyleft)',
  unlicensed: 'var(--license-weak_copyleft)',
  declared: 'var(--license-other)',
  unknown: 'var(--license-unknown)',
  unassessed: 'var(--license-unknown)',
}

const familyConfig = Object.fromEntries(
  LICENSE_FAMILIES.map((family) => [family, { label: FAMILY_LABEL[family], color: `var(--license-${family})` }]),
) as ChartConfig

const shorten = (value: string, length: number) => (value.length > length ? `${value.slice(0, length - 1)}…` : value)

const componentsLink = (stream: string, license: string) => `/supply-chain/components?${new URLSearchParams({ stream, license }).toString()}`

type MixRow = { name: string; detail: RepositoryMix } & Record<LicenseFamily, number>

function MixTooltip({ active, payload }: { active?: boolean; payload?: readonly { payload?: unknown }[] }) {
  const row = payload?.[0]?.payload as MixRow | undefined
  if (!active || !row) return null
  return (
    <div className="grid max-w-sm gap-1.5 rounded-lg border bg-background px-2.5 py-1.5 text-xs shadow-xl">
      <p className="font-medium break-all">{row.name}</p>
      {LICENSE_FAMILIES.filter((family) => row.detail.families[family] > 0).map((family) => (
        <div key={family}>
          <p>
            {FAMILY_LABEL[family]}: <span className="font-mono">{row.detail.families[family]}</span>
          </p>
          <p className="break-all text-muted-foreground">{row.detail.expressions[family].map((item) => `${item.expression} (${item.count})`).join(', ')}</p>
        </div>
      ))}
    </div>
  )
}

function Kpis({ overview }: { overview: SupplyChainOverview }) {
  const { repositories, components } = overview
  const totals = assessmentTotals(overview)
  const coordinates = 'component occurrences with an assessment status'
  const authorized = 'authorized repositories'
  return (
    <div className="@container" role="group" aria-label="Key figures">
      <div className="grid grid-cols-2 gap-3 @2xl:grid-cols-4 @5xl:grid-cols-7">
      <MetricCard label="Repositories with inventory" value={repositories.with_inventory} denominator={denominatorText(repositories.with_inventory, repositories.authorized, authorized)} />
      <MetricCard label="Unique coordinates" value={components.unique_coordinates} denominator={denominatorText(components.unique_coordinates, components.occurrences, 'occurrences')} />
      <MetricCard
        label="Assessed share"
        value={totals.assessedPercent === undefined ? '—' : `${totals.assessedPercent}%`}
        denominator={denominatorText(totals.assessed, totals.total, coordinates)}
      />
      <MetricCard label="Conflicts" value={components.assessments?.conflict ?? 0} denominator={denominatorText(components.assessments?.conflict ?? 0, totals.total, coordinates)} />
      <MetricCard label="Unlicensed" value={components.assessments?.unlicensed ?? 0} denominator={denominatorText(components.assessments?.unlicensed ?? 0, totals.total, coordinates)} />
      <MetricCard label="Never collected" value={repositories.never_collected} denominator={denominatorText(repositories.never_collected, repositories.authorized, authorized)} />
      <MetricCard label="Stale" value={repositories.stale} denominator={denominatorText(repositories.stale, repositories.authorized, authorized)} />
      </div>
    </div>
  )
}

function StatusDonut({ overview }: { overview: SupplyChainOverview }) {
  const slices = statusSlices(overview)
  const config = Object.fromEntries(slices.map(({ status }, index) => [status, { label: status, color: STATUS_COLOR[status] ?? chartColor(index) }])) as ChartConfig
  const total = assessmentTotals(overview).total
  return (
    <ChartCard
      title="Assessment status"
      description={`Component occurrences by assessment status, out of ${total} component occurrences with an assessment status. Evidence, not compliance.`}
      table={{ head: ['Status', 'Occurrences'], rows: slices.map(({ status, count }) => [status, count]) }}
    >
      {(labels) =>
        slices.length ? (
          <ChartContainer config={config} className="aspect-auto h-72 w-full" {...labels}>
            <PieChart>
              <ChartTooltip content={<ChartTooltipContent nameKey="status" hideLabel />} />
              <Pie data={slices} dataKey="count" nameKey="status" innerRadius="55%" isAnimationActive={false}>
                {slices.map(({ status }) => (
                  <Cell key={status} fill={`var(--color-${status})`} />
                ))}
              </Pie>
              <ChartLegend content={<ChartLegendContent nameKey="status" />} />
            </PieChart>
          </ChartContainer>
        ) : (
          <p className="text-sm text-muted-foreground">No assessments have been recorded.</p>
        )
      }
    </ChartCard>
  )
}

const barHeight = (rows: number) => ({ height: `${Math.max(rows, 1) * 32 + 48}px` })

function LicenseBars({ facets, stream, occurrences }: { facets: SupplyChainFacets; stream: string; occurrences: number }) {
  const navigate = useNavigate()
  const rows = topLicenses(facets)
  const [asTable, setAsTable] = useState(false)
  const config = { count: { label: 'Components', color: 'var(--primary)' } } satisfies ChartConfig
  return (
    <ChartCard
      title="Top license expressions"
      description={`The ${rows.length} most common normalized license expressions, by components, out of ${occurrences} component occurrences in scope (at most 50 expressions are listed). Each bar opens the components list filtered to that expression.`}
      table={{ head: ['License expression', 'Components'], rows: rows.map((row) => [row.value, row.count]) }}
    >
      {(labels) => (
        <>
          {rows.length === 0 && <p className="text-sm text-muted-foreground">No license expressions have been recorded.</p>}
          {rows.length > 0 && (
            <div className="flex items-center gap-2">
              <Switch id="sc-license-table" checked={asTable} onCheckedChange={setAsTable} />
              <Label htmlFor="sc-license-table">Show as table</Label>
            </div>
          )}
          {rows.length > 0 && !asTable && (
            <ChartContainer config={config} className="aspect-auto w-full" style={barHeight(rows.length)} {...labels}>
              <BarChart data={rows} layout="vertical" margin={{ left: 8 }}>
                <CartesianGrid horizontal={false} />
                <YAxis dataKey="value" type="category" width={150} tickLine={false} axisLine={false} tickFormatter={(value: string) => shorten(value, 24)} />
                <XAxis type="number" allowDecimals={false} />
                <ChartTooltip content={<ChartTooltipContent hideLabel />} />
                <Bar
                  dataKey="count"
                  fill="var(--color-count)"
                  radius={3}
                  isAnimationActive={false}
                  className="cursor-pointer"
                  onClick={(_, index) => void navigate(componentsLink(stream, rows[index].value))}
                />
              </BarChart>
            </ChartContainer>
          )}
          {rows.length > 0 && asTable && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>License expression</TableHead>
                  <TableHead className="text-right">Components</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((row) => (
                  <TableRow key={row.value}>
                    <TableCell className="font-mono break-all whitespace-normal">
                      <Link className="underline underline-offset-2" to={componentsLink(stream, row.value)}>
                        {row.value}
                        <span className="sr-only">: show components</span>
                      </Link>
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{row.count}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </>
      )}
    </ChartCard>
  )
}

function FamilyBars({ rows, withLicenses, inspected, topN }: { rows: RepositoryMix[]; withLicenses: number; inspected: number; topN: number }) {
  const data: MixRow[] = rows.map((detail) => ({ name: detail.name, detail, ...detail.families }))
  return (
    <ChartCard
      title="License families per repository"
      description={`License mix of the top ${topN} of ${withLicenses} repositories with license expressions, among ${inspected} repositories read, stacked by license family. Counts are components with a normalized license expression.`}
      table={{
        head: ['Repository', ...LICENSE_FAMILIES.map((family) => FAMILY_LABEL[family]), 'Expressions'],
        rows: rows.map((row) => [
          row.name,
          ...LICENSE_FAMILIES.map((family) => row.families[family]),
          LICENSE_FAMILIES.flatMap((family) => row.expressions[family].map((item) => `${item.expression} (${item.count})`)).join(', '),
        ]),
      }}
    >
      {(labels) =>
        rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">No repository in scope has license expressions.</p>
        ) : (
          <ChartContainer config={familyConfig} className="aspect-auto w-full" style={barHeight(rows.length)} {...labels}>
            <BarChart data={data} layout="vertical" margin={{ left: 8 }}>
              <CartesianGrid horizontal={false} />
              <YAxis dataKey="name" type="category" width={150} tickLine={false} axisLine={false} tickFormatter={(value: string) => shorten(value, 24)} />
              <XAxis type="number" allowDecimals={false} />
              <ChartTooltip content={MixTooltip} />
              <ChartLegend itemSorter={null} content={<ChartLegendContent />} />
              {LICENSE_FAMILIES.map((family) => (
                <Bar key={family} dataKey={family} stackId="mix" fill={`var(--color-${family})`} isAnimationActive={false} />
              ))}
            </BarChart>
          </ChartContainer>
        )
      }
    </ChartCard>
  )
}

function EcosystemBars({ facets, occurrences }: { facets: SupplyChainFacets; occurrences: number }) {
  const rows = [...facets.ecosystems].sort((a, b) => b.count - a.count || a.value.localeCompare(b.value))
  const config = { count: { label: 'Components', color: 'var(--primary)' } } satisfies ChartConfig
  return (
    <ChartCard
      title="Components per ecosystem"
      description={`Components grouped by package ecosystem, out of ${occurrences} component occurrences in scope (at most 50 ecosystems are listed).`}
      table={{ head: ['Ecosystem', 'Components'], rows: rows.map((row) => [row.value, row.count]) }}
    >
      {(labels) =>
        rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">No ecosystems have been observed.</p>
        ) : (
          <ChartContainer config={config} className="aspect-auto w-full" style={barHeight(rows.length)} {...labels}>
            <BarChart data={rows} layout="vertical" margin={{ left: 8 }}>
              <CartesianGrid horizontal={false} />
              <YAxis dataKey="value" type="category" width={90} tickLine={false} axisLine={false} />
              <XAxis type="number" allowDecimals={false} />
              <ChartTooltip content={<ChartTooltipContent hideLabel />} />
              <Bar dataKey="count" fill="var(--color-count)" radius={3} isAnimationActive={false} />
            </BarChart>
          </ChartContainer>
        )
      }
    </ChartCard>
  )
}

function FreshnessStrip({ overview }: { overview: SupplyChainOverview }) {
  const { repositories } = overview
  return (
    <Panel title="Freshness">
      <div className="grid grid-cols-2 gap-x-6 gap-y-1 md:grid-cols-4">
        <p>Oldest collection: {when(overview.oldest_collected_at)}</p>
        <p>Newest collection: {when(overview.newest_collected_at)}</p>
        <p>Failed last attempt: {denominatorText(repositories.failed_last_attempt, repositories.authorized, 'authorized repositories')}</p>
        <p>Opted out: {denominatorText(repositories.opted_out, repositories.authorized, 'authorized repositories')}</p>
      </div>
    </Panel>
  )
}

const parseScope = (value: string | null) =>
  (value ?? '')
    .split(',')
    .map(Number)
    .filter((id) => Number.isSafeInteger(id) && id > 0)

export default function LicensesView() {
  const { params, stream, update } = useViewParams()
  const scope = useMemo(() => parseScope(params.get('repos')).slice(0, REPOSITORY_FACET_LIMIT), [params])
  const topN = TOP_N_CHOICES.some((choice) => choice.value === params.get('top')) ? Number(params.get('top')) : DEFAULT_TOP_N
  const scoped = scope.length > 0

  const repositories = useSupplyChainRepositories()
  const overview = useQuery({
    queryKey: ['supply-chain', 'overview', stream, scope],
    queryFn: ({ signal }) => getSupplyChainOverview({ stream, repository_id: scoped ? scope : undefined }, signal),
    retry: false,
  })
  const wholeFacets = useQuery({
    queryKey: ['supply-chain', 'facets', stream],
    queryFn: ({ signal }) => getSupplyChainFacets({ stream }, signal),
    enabled: !scoped,
    retry: false,
  })

  const authorized = useMemo(() => repositories.data ?? [], [repositories.data])
  const targets = useMemo(
    () => (scoped ? authorized.filter((repository) => scope.includes(repository.github_id)) : authorized.slice(0, REPOSITORY_FACET_LIMIT)),
    [authorized, scope, scoped],
  )
  const perRepository = useQueries({
    queries: targets.map((repository) => ({
      queryKey: ['supply-chain', 'facets', stream, repository.github_id],
      queryFn: ({ signal }: { signal: AbortSignal }) => getSupplyChainFacets({ stream, repository_id: repository.github_id }, signal),
      retry: false,
      staleTime: 60_000,
    })),
  })

  const settled = repositories.isSuccess && perRepository.every((query) => !query.isPending)
  const failed = perRepository.filter((query) => query.isError).length
  const entries: RepositoryFacets[] = settled
    ? targets.flatMap((repository, index) => {
        const facets = perRepository[index].data
        return facets ? [{ repositoryId: repository.github_id, name: repository.name, facets }] : []
      })
    : []
  const mix = repositoryLicenseMix(entries, topN)
  const scopeFacets: SupplyChainFacets | undefined = scoped ? (settled ? sumFacets(stream, entries.map((entry) => entry.facets)) : undefined) : wholeFacets.data

  const disabled = [overview.error, wholeFacets.error].find(isDisabled)
  const data = overview.data
  const error = overview.error ?? wholeFacets.error ?? repositories.error

  return (
    <div className="grid gap-4">
      <ViewHeader title="Licenses" />
      <Alert>
        <AlertTitle>Evidence, not compliance</AlertTitle>
        <AlertDescription>
          Assessments describe the evidence GraphNest collected about each dependency. They are not a compliance verdict, a legal opinion or an approval.
        </AlertDescription>
      </Alert>
      <div className="flex flex-wrap items-end gap-3">
        <StreamSelect stream={stream} onChange={(value) => update({ stream: value })} />
        <ScopePicker
          choices={authorized.map((repository) => ({ id: repository.github_id, name: repository.name }))}
          selected={scope}
          limit={REPOSITORY_FACET_LIMIT}
          onChange={(ids) => update({ repos: ids.join(',') })}
        />
        <ChoiceSelect id="sc-top-n" label="Repositories in the family chart" value={String(topN)} choices={TOP_N_CHOICES} onChange={(value) => update({ top: value })} className="w-60" />
      </div>
      {disabled ? (
        <DisabledModule error={disabled} />
      ) : (
        <>
          <div aria-live="polite">{(overview.isPending || (!scoped && wholeFacets.isPending)) && <StateMessage>Loading license data…</StateMessage>}</div>
          {error && <ErrorNotice error={error} onRetry={() => void Promise.all([overview.refetch(), repositories.refetch()])} />}
          {overview.isPending && <Skeleton className="h-32 w-full" aria-label="Loading license data" />}
          {data && (
            <>
              {data.repositories.authorized === 0 && <StateMessage>No authorized repositories are in scope</StateMessage>}
              <Kpis overview={data} />
              <div className="grid gap-4 xl:grid-cols-2">
                <StatusDonut overview={data} />
                {scopeFacets ? <LicenseBars facets={scopeFacets} stream={stream} occurrences={data.components.occurrences} /> : <Skeleton className="h-64 w-full" aria-label="Loading license expressions" />}
                {settled ? (
                  <FamilyBars rows={mix.rows} withLicenses={mix.withLicenses} inspected={targets.length} topN={topN} />
                ) : (
                  <Skeleton className="h-64 w-full" aria-label="Loading repository license mix" />
                )}
                {scopeFacets ? <EcosystemBars facets={scopeFacets} occurrences={data.components.occurrences} /> : <Skeleton className="h-64 w-full" aria-label="Loading ecosystems" />}
              </div>
              {failed > 0 && (
                <Alert variant="destructive" aria-live="polite">
                  <AlertDescription>{failed} of {targets.length} repositories could not be read and are missing from the per-repository figures.</AlertDescription>
                </Alert>
              )}
              {!scoped && authorized.length > REPOSITORY_FACET_LIMIT && (
                <StateMessage>
                  Per-repository figures cover the first {REPOSITORY_FACET_LIMIT} of {authorized.length} authorized repositories; choose a repository scope to inspect others.
                </StateMessage>
              )}
              <FreshnessStrip overview={data} />
              <Panel title="How these numbers are counted">
                {data.denominators.length ? (
                  data.denominators.map((line, index) => (
                    <p key={index} className="text-muted-foreground">
                      {text(line)}
                    </p>
                  ))
                ) : (
                  <p className="text-muted-foreground">No denominators were reported.</p>
                )}
              </Panel>
            </>
          )}
        </>
      )}
    </div>
  )
}
