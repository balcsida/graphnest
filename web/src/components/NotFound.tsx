import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

export function NotFound() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Page not found</CardTitle>
        <CardDescription>This page does not exist in the console.</CardDescription>
      </CardHeader>
      <CardContent>
        <Button asChild>
          <Link to="/">Back to search</Link>
        </Button>
      </CardContent>
    </Card>
  )
}
