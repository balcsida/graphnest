/** Always mounted so the polite live region announces the token when it appears. */
export function TokenReveal({ token }: { token: string }) {
  return (
    <p aria-live="polite" className="font-mono text-sm break-all">
      {token ? `Copy this token now; it will not be shown again: ${token}` : ''}
    </p>
  )
}
