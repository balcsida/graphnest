import { ChevronsUpDown } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'

export interface ScopeChoice {
  id: number
  name: string
}

/** Multi-select of authorized repositories; an empty selection means every authorized repository. */
export function ScopePicker({
  choices,
  selected,
  limit,
  onChange,
}: {
  choices: ScopeChoice[]
  selected: number[]
  limit: number
  onChange: (selected: number[]) => void
}) {
  const full = selected.length >= limit
  const toggle = (id: number, checked: boolean) => onChange(checked ? [...selected, id] : selected.filter((item) => item !== id))
  return (
    <div className="grid gap-1.5">
      <Label id="sc-scope-label" htmlFor="sc-scope">
        Repository scope
      </Label>
      <Popover>
        <PopoverTrigger asChild>
          <Button id="sc-scope" aria-labelledby="sc-scope-label sc-scope" type="button" variant="outline" className="w-64 justify-between">
            {selected.length ? `${selected.length} selected` : 'All authorized repositories'}
            <ChevronsUpDown aria-hidden="true" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="grid w-80 gap-2">
          <p className="text-sm text-muted-foreground">Choose at most {limit} repositories, or none for every authorized repository.</p>
          <div className="grid max-h-72 gap-2 overflow-auto" role="group" aria-label="Repositories">
            {choices.map((choice) => {
              const checked = selected.includes(choice.id)
              return (
                <div key={choice.id} className="flex items-center gap-2">
                  <Checkbox id={`sc-scope-${choice.id}`} checked={checked} disabled={!checked && full} onCheckedChange={(value) => toggle(choice.id, value === true)} />
                  <Label htmlFor={`sc-scope-${choice.id}`} className="break-all font-normal">
                    {choice.name}
                  </Label>
                </div>
              )
            })}
            {choices.length === 0 && <p className="text-sm text-muted-foreground">No authorized repositories.</p>}
          </div>
          <Button type="button" variant="ghost" size="sm" className="justify-self-start" disabled={selected.length === 0} onClick={() => onChange([])}>
            Clear selection
          </Button>
        </PopoverContent>
      </Popover>
    </div>
  )
}
