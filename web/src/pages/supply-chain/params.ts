import { useCallback } from 'react'
import { useSearchParams } from 'react-router'
import { DEFAULT_STREAM } from '@/api/supply-chain'

const validStream = (value: string | null) => (value && (value === DEFAULT_STREAM || value.startsWith('import:')) ? value : DEFAULT_STREAM)

/** Positive whole number from a search parameter, or undefined. */
export const idParam = (value: string | null): number | undefined => {
  const id = Number(value)
  return value && Number.isSafeInteger(id) && id > 0 ? id : undefined
}

/**
 * URL-backed view state shared by every supply-chain view: the stream is a search parameter so that links,
 * reloads and the back button keep it. `update` changes some parameters and drops the empty ones.
 */
export function useViewParams() {
  const [params, setParams] = useSearchParams()
  const update = useCallback(
    (changes: Record<string, string | undefined>) =>
      setParams(
        (current) => {
          const next = new URLSearchParams(current)
          for (const [name, value] of Object.entries(changes)) {
            if (value) next.set(name, value)
            else next.delete(name)
          }
          return next
        },
        { replace: true },
      ),
    [setParams],
  )
  return { params, stream: validStream(params.get('stream')), update }
}
