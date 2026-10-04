import { useQuery } from '@tanstack/react-query'
import { getAuditEvents } from '@/api/admin'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { DASH, bounded, when } from './format'
import { EmptyRow, PartialNotice, QueryBoundary, StatusPill, mono } from './shared'

export function AuditSection() {
  const query = useQuery({ queryKey: ['admin', 'audit'], queryFn: ({ signal }) => getAuditEvents(signal), refetchInterval: 30_000, retry: false })
  return (
    <QueryBoundary query={query} label="audit events">
      {(data) => {
        const events = bounded(data.events)
        return (
          <div className="grid gap-4">
            {data.truncated && <PartialNotice names={['Audit events']} />}
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead scope="col">Time</TableHead>
                  <TableHead scope="col">Operation</TableHead>
                  <TableHead scope="col">Outcome</TableHead>
                  <TableHead scope="col">Actor</TableHead>
                  <TableHead scope="col">Target</TableHead>
                  <TableHead scope="col">Method</TableHead>
                  <TableHead scope="col">Request</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {events.length === 0 && <EmptyRow columns={7}>No audit events are available.</EmptyRow>}
                {events.map((event, index) => (
                  <TableRow key={`${event.request_id}-${event.created_at}-${index}`}>
                    <TableCell>{when(event.created_at)}</TableCell>
                    <TableCell className={mono}>{event.operation}</TableCell>
                    <TableCell>
                      <StatusPill value={event.outcome} />
                    </TableCell>
                    <TableCell className="break-all whitespace-normal">
                      {event.actor_type}:{event.actor_id}
                    </TableCell>
                    <TableCell className="break-all whitespace-normal">
                      {event.target_type}:{event.target_id}
                    </TableCell>
                    <TableCell>{event.authentication_method || DASH}</TableCell>
                    <TableCell className="break-all whitespace-normal">{event.request_id || DASH}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )
      }}
    </QueryBoundary>
  )
}
