import { useState, type FormEvent } from 'react'
import { localRotate, localSignIn } from '@/api/auth'
import type { AuthProvider as LoginProvider } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { ApiError } from '@/lib/api'
import { useAuth } from '@/lib/auth'

function TokenForm() {
  const { signInWithToken } = useAuth()
  const [token, setToken] = useState('')
  const [failed, setFailed] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    const trimmed = token.trim()
    if (!trimmed) return
    setFailed(false)
    try {
      await signInWithToken(trimmed)
      setToken('')
    } catch {
      setToken('')
      setFailed(true)
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-3">
      <Label htmlFor="bearer-token">Bearer token</Label>
      <Input
        id="bearer-token"
        type="password"
        autoComplete="off"
        value={token}
        aria-invalid={failed}
        onChange={(e) => {
          setToken(e.target.value)
          setFailed(false)
        }}
        required
      />
      <p className="text-xs text-muted-foreground">Kept for this tab only. Sent only to this origin.</p>
      <Button type="submit">Connect</Button>
      {failed && <p role="alert" className="text-sm text-destructive">Failed.</p>}
    </form>
  )
}

function RecoveryForm() {
  const { refresh } = useAuth()
  const [user, setUser] = useState('')
  const [password, setPassword] = useState('')
  const [replacement, setReplacement] = useState('')
  const [message, setMessage] = useState('')

  async function submit(event: FormEvent) {
    event.preventDefault()
    const current = password
    const next = replacement
    setPassword('')
    setReplacement('')
    setMessage('')
    try {
      await (next ? localRotate(user, current, next) : localSignIn(user, current))
      await refresh()
    } catch (failure) {
      setMessage(failure instanceof ApiError && failure.status === 429 ? 'Try again later.' : 'Sign-in failed.')
    }
  }

  return (
    <details>
      <summary className="cursor-pointer text-sm font-medium">Administrator recovery</summary>
      <form onSubmit={submit} className="mt-3 grid gap-3" aria-label="Administrator recovery">
        <Label htmlFor="local-user">User name</Label>
        <Input id="local-user" autoComplete="username" value={user} onChange={(e) => setUser(e.target.value)} required />
        <Label htmlFor="local-password">Current password</Label>
        <Input id="local-password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        <Label htmlFor="local-new-password">New password (only when rotation is required)</Label>
        <Input
          id="local-new-password"
          type="password"
          autoComplete="new-password"
          minLength={16}
          maxLength={1024}
          value={replacement}
          onChange={(e) => setReplacement(e.target.value)}
        />
        <Button type="submit" variant="secondary">Continue</Button>
        <p role="status" aria-live="polite" className="text-sm text-destructive">{message}</p>
      </form>
    </details>
  )
}

/** Provider links are same-origin paths only; anything else is dropped. */
function sameOriginProviders(providers: LoginProvider[]) {
  return providers.flatMap((provider) => {
    if (typeof provider.login_url !== 'string') return []
    try {
      const url = new URL(provider.login_url, window.location.origin)
      if (url.origin !== window.location.origin) return []
      return [{ id: provider.id, label: provider.label || 'Sign in with SSO', href: url.pathname + url.search + url.hash, github: /github/i.test(url.pathname) }]
    } catch {
      return []
    }
  })
}

export function TokenGate() {
  const { token_login, break_glass, providers, error } = useAuth()
  const links = sameOriginProviders(providers)
  return (
    <main className="flex min-h-svh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <p className="flex items-center gap-2 font-semibold">
            <span aria-hidden="true" className="font-mono">{'{}'}</span> GraphNest
          </p>
          <h1 className="leading-none font-semibold">Sign in</h1>
          <CardDescription>Search and navigate the code you&apos;re authorized to see.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          {links.length > 0 && (
            <div className="grid gap-2">
              {links.map((link) => (
                <Button key={link.id} variant={link.github ? 'default' : 'outline'} asChild>
                  <a href={link.href}>{link.label}</a>
                </Button>
              ))}
            </div>
          )}
          {token_login && (
            <>
              <div className="flex items-center gap-3 text-xs text-muted-foreground">
                <Separator className="flex-1" />
                or use a token
                <Separator className="flex-1" />
              </div>
              <TokenForm />
            </>
          )}
          {break_glass && <RecoveryForm />}
          {links.length === 0 && !token_login && !break_glass && <p className="text-sm text-muted-foreground">No sign-in method is enabled on this server.</p>}
        </CardContent>
      </Card>
    </main>
  )
}
