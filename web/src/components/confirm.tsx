import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

interface Pending {
  message: string
  resolve: (confirmed: boolean) => void
}

/**
 * Replaces window.confirm: `await confirm(message)` resolves true on Confirm and false on Cancel,
 * Escape or outside click. Render `dialog` once in the component that calls `confirm`.
 */
export function useConfirm(): { confirm: (message: string) => Promise<boolean>; dialog: ReactNode } {
  const [pending, setPending] = useState<Pending | null>(null)
  const current = useRef<Pending | null>(null)

  const settle = useCallback((confirmed: boolean) => {
    current.current?.resolve(confirmed)
    current.current = null
    setPending(null)
  }, [])

  const confirm = useCallback(
    (message: string) =>
      new Promise<boolean>((resolve) => {
        current.current?.resolve(false)
        current.current = { message, resolve }
        setPending(current.current)
      }),
    [],
  )

  useEffect(() => () => current.current?.resolve(false), [])

  const dialog = (
    <Dialog open={pending !== null} onOpenChange={(open) => !open && settle(false)}>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>Confirm action</DialogTitle>
          <DialogDescription>{pending?.message}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => settle(false)}>
            Cancel
          </Button>
          <Button type="button" onClick={() => settle(true)}>
            Confirm
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
  return { confirm, dialog }
}
