import { useId, type ReactNode } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

export interface ChartLabels {
  role: 'img'
  'aria-labelledby': string
  'aria-describedby': string
}

/**
 * A chart with an accessible title and description. `table` is the same data as a screen-reader table; the
 * chart itself is announced as one image named by the title and described by the description.
 */
export function ChartCard({
  title,
  description,
  table,
  children,
}: {
  title: string
  description: string
  table: { head: string[]; rows: ReactNode[][] }
  children: (labels: ChartLabels) => ReactNode
}) {
  const id = useId()
  const labels: ChartLabels = { role: 'img', 'aria-labelledby': `${id}-title`, 'aria-describedby': `${id}-description` }
  return (
    <Card>
      <CardHeader>
        <CardTitle id={labels['aria-labelledby']} className="text-sm">
          {title}
        </CardTitle>
        <CardDescription id={labels['aria-describedby']}>{description}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        {children(labels)}
        <Table className="sr-only">
          <caption>{title}</caption>
          <TableHeader>
            <TableRow>
              {table.head.map((name) => (
                <TableHead key={name}>{name}</TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {table.rows.map((row, index) => (
              <TableRow key={index}>
                {row.map((cell, column) => (
                  <TableCell key={column}>{cell}</TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}
