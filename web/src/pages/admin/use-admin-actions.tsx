import { useCallback, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { useConfirm } from '@/components/confirm'
import { ApiError } from '@/lib/api'
import { errorMessage } from './format'

/**
 * Confirm, call, report, refresh: the legacy `confirmed()` flow. `confirmed` resolves true only
 * after the call succeeded. A 401 already dropped to the gate, so it is not repeated as a toast.
 */
export function useAdminActions(): {
  confirmed: <T,>(message: string, call: () => Promise<T>, success: string, scope?: string, onSuccess?: (value: T) => void) => Promise<boolean>
  confirm: (message: string) => Promise<boolean>
  refresh: (scope?: string) => Promise<void>
  dialog: ReactNode
} {
  const { confirm, dialog } = useConfirm()
  const queryClient = useQueryClient()
  const refresh = useCallback((scope = 'admin') => queryClient.invalidateQueries({ queryKey: [scope] }), [queryClient])
  const confirmed = useCallback(
    async <T,>(message: string, call: () => Promise<T>, success: string, scope = 'admin', onSuccess?: (value: T) => void) => {
      if (!(await confirm(message))) return false
      let value: T
      try {
        value = await call()
      } catch (failure) {
        if (!(failure instanceof ApiError && failure.status === 401)) toast.error(errorMessage(failure))
        return false
      }
      onSuccess?.(value)
      toast.success(success)
      await refresh(scope)
      return true
    },
    [confirm, refresh],
  )
  return { confirmed, confirm, refresh, dialog }
}
