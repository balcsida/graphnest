import { useQuery } from '@tanstack/react-query'
import { getWebhookDeliveries } from '@/api/admin'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { DASH, bounded, short, when } from './format'
import { EmptyRow, PartialNotice, QueryBoundary, StatusPill } from './shared'

export function DeliveriesSection() {
  const query = useQuery({ queryKey: ['admin', 'deliveries'], queryFn: ({ signal }) => getWebhookDeliveries(signal), refetchInterval: 30_000, retry: false })
  return (
    <QueryBoundary query={query} label="webhook deliveries">
      {(data) => {
        const deliveries = bounded(data.deliveries)
        return (
          <div className="grid gap-4">
            {data.truncated && <PartialNotice names={['webhooks']} />}
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead scope="col">Delivery</TableHead>
                  <TableHead scope="col">Event</TableHead>
                  <TableHead scope="col">State</TableHead>
                  <TableHead scope="col">Installation</TableHead>
                  <TableHead scope="col">Outcome</TableHead>
                  <TableHead scope="col">Received</TableHead>
                  <TableHead scope="col">Processed</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {deliveries.length === 0 && <EmptyRow columns={7}>No webhook deliveries are available.</EmptyRow>}
                {deliveries.map((delivery) => (
                  <TableRow key={delivery.delivery_id}>
                    <TableCell className="font-mono">{short(delivery.delivery_id)}</TableCell>
                    <TableCell>{delivery.event}</TableCell>
                    <TableCell>
                      <StatusPill value={delivery.state} />
                    </TableCell>
                    <TableCell>{String(delivery.installation_id || DASH)}</TableCell>
                    <TableCell>{delivery.error_code || 'Processed'}</TableCell>
                    <TableCell>{when(delivery.received_at)}</TableCell>
                    <TableCell>{when(delivery.processed_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <p className="rounded-md border p-3 text-sm text-muted-foreground">
              <code>POST /webhooks/github</code> verifies GitHub HMAC signatures before processing.
            </p>
          </div>
        )
      }}
    </QueryBoundary>
  )
}
