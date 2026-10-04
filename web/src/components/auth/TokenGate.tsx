import { useState, type FormEvent } from 'react'
import { localRotate, localSignIn } from '@/api/auth'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { ApiError } from '@/lib/api'
import { useAuth } from '@/lib/auth'

function TokenForm() {
  const { signInWithToken } = useAuth()
  const [token, setToken] = useState('')
  const [message, setMessage] = useState('')

  async function submit(event: FormEvent) {
    event.preventDefault()
    setMessage('')
    try {
      await signInWithToken(token.trim())
      setToken('')
    } catch (failure) {
      setToken('')
      setMessage(failure instanceof ApiError && failure.status === 401 ? 'Token rejected.' : 'Sign-in failed.')
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-3">
      <Label htmlFor="bearer-token">API token</Label>
      <Input id="bearer-token" type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} required />
      <Button type="submit">Sign in with token</Button>
      {message && <p role="alert" className="text-sm text-destructive">{message}</p>}
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
    <form onSubmit={submit} className="grid gap-3" aria-label="Administrator recovery">
      <h2 className="text-sm font-medium">Administrator recovery</h2>
      <Label htmlFor="local-user">User name</Label>
      <Input id="local-user" autoComplete="username" value={user} onChange={(e) => setUser(e.target.value)} required />
      <Label htmlFor="local-password">Password</Label>
      <Input id="local-password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
      <Label htmlFor="local-new-password">New password (optional, rotates the password)</Label>
      <Input id="local-new-password" type="password" autoComplete="new-password" value={replacement} onChange={(e) => setReplacement(e.target.value)} />
      <Button type="submit" variant="secondary">Sign in as administrator</Button>
      {message && <p role="alert" className="text-sm text-destructive">{message}</p>}
    </form>
  )
}

export function TokenGate() {
  const { token_login, break_glass, providers, error } = useAuth()
  const sections = [
    providers.length > 0 && (
      <div key="providers" className="grid gap-2">
        {providers.map((provider) => (
          <Button key={provider.id} variant="outline" asChild>
            <a href={provider.login_url}>{provider.label}</a>
          </Button>
        ))}
      </div>
    ),
    token_login && <TokenForm key="token" />,
    break_glass && <RecoveryForm key="recovery" />,
  ].filter(Boolean)

  return (
    <main className="flex min-h-svh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>GraphNest</CardTitle>
          <CardDescription>Sign in to search your indexed repositories.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          {sections.map((section, index) => (
            <div key={index} className="grid gap-4">
              {index > 0 && <Separator />}
              {section}
            </div>
          ))}
          {sections.length === 0 && <p className="text-sm text-muted-foreground">No sign-in method is enabled on this server.</p>}
        </CardContent>
      </Card>
    </main>
  )
}
