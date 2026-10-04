import { Handle, Position, type NodeProps } from '@xyflow/react'
import { cn } from '@/lib/utils'
import { ecosystemColor, type DependencyFlowNode, type RepositoryFlowNode } from './build-graph'

const transition = 'transition-opacity motion-reduce:transition-none'

export function RepositoryNode({ data }: NodeProps<RepositoryFlowNode>) {
  return (
    <div className={cn('flex h-full w-full flex-col justify-center rounded-md border bg-card px-3 text-card-foreground shadow-xs', transition, data.dimmed && 'opacity-30')}>
      <p className="truncate text-sm font-medium" title={data.repository.name}>
        {data.repository.name}
      </p>
      <p className="text-xs text-muted-foreground">{data.repository.dependencyIds.length} shown dependencies</p>
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  )
}

export function DependencyNode({ data }: NodeProps<DependencyFlowNode>) {
  const { dependency } = data
  return (
    <div
      className={cn(
        'flex h-full w-full flex-col justify-center rounded-md border-2 bg-card px-3 text-card-foreground shadow-xs',
        dependency.risky && 'border-destructive',
        transition,
        data.dimmed && 'opacity-30',
      )}
      style={dependency.risky ? undefined : { borderColor: ecosystemColor(data.colorIndex) }}
    >
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <p className="truncate text-sm font-medium" title={dependency.label}>
        {dependency.label}
      </p>
      <p className="flex items-center gap-1.5 truncate text-xs text-muted-foreground">
        <span aria-hidden="true" className="size-2 shrink-0 rounded-full" style={{ backgroundColor: ecosystemColor(data.colorIndex) }} />
        {dependency.ecosystem} · {dependency.repositoryIds.length} repositories{dependency.risky ? ' · conflict or unlicensed' : ''}
      </p>
    </div>
  )
}
