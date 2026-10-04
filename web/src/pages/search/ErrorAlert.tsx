import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { describeError } from './model'

/** Structured API error: server message, request ID, and Retry only when the server says it is retryable. */
export function ErrorAlert({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const { message, requestId, retryable } = describeError(error)
  return (
    <Alert variant="destructive" aria-live="assertive" aria-atomic="true">
      <AlertDescription>
        <p>
          {message}
          {requestId && <small> Request ID: {requestId}</small>}
        </p>
        {retryable && onRetry && (
          <Button type="button" size="sm" variant="outline" onClick={onRetry}>
            Retry
          </Button>
        )}
      </AlertDescription>
    </Alert>
  )
}
