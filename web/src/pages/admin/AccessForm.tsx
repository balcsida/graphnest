import type { FormEvent, RefObject } from 'react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { AccessDraft } from './access-draft'
import { Panel } from './shared'

/** The "Replace direct access" and "Replace group access" editors; `kind` prefixes the control ids. */
export function AccessForm({
  kind,
  title,
  idLabel,
  submitLabel,
  draft,
  idRef,
  error,
  onChange,
  onSubmit,
}: {
  kind: 'user' | 'group'
  title: string
  idLabel: string
  submitLabel: string
  draft: AccessDraft
  idRef: RefObject<HTMLInputElement | null>
  error: string
  onChange: (draft: AccessDraft) => void
  onSubmit: () => void
}) {
  const submit = (event: FormEvent) => {
    event.preventDefault()
    onSubmit()
  }
  return (
    <Panel title={title}>
      <form className="grid gap-3" onSubmit={submit}>
        <div className="grid gap-1.5">
          <Label htmlFor={`${kind}-id`}>{idLabel}</Label>
          <Input id={`${kind}-id`} ref={idRef} inputMode="numeric" required value={draft.id} onChange={(event) => onChange({ ...draft, id: event.target.value })} />
        </div>
        <div className="flex items-center gap-2">
          <Checkbox id={`${kind}-admin`} checked={draft.administrator} onCheckedChange={(checked) => onChange({ ...draft, administrator: checked === true })} />
          <Label htmlFor={`${kind}-admin`}>Administrator</Label>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={`${kind}-repositories`}>Repository IDs (comma-separated)</Label>
          <Input id={`${kind}-repositories`} inputMode="numeric" value={draft.repositories} onChange={(event) => onChange({ ...draft, repositories: event.target.value })} />
        </div>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <Button type="submit">{submitLabel}</Button>
      </form>
    </Panel>
  )
}
