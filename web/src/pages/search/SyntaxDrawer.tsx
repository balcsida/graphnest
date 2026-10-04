import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Sheet, SheetClose, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'

const syntaxExamples = ['file:\\.go$', 'lang:go', 'repo:payments', 'case:yes NewService', '-vendor/', '"exact phrase"', 'New(Service|Client)']

export function SyntaxDrawer({
  open,
  onOpenChange,
  onCloseAutoFocus,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCloseAutoFocus: (event: Event) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" showCloseButton={false} onCloseAutoFocus={onCloseAutoFocus} className="sm:max-w-[360px]">
        <SheetHeader className="flex-row items-center justify-between">
          <SheetTitle>Query syntax</SheetTitle>
          <SheetClose asChild>
            <Button type="button" variant="ghost" size="icon" aria-label="Close syntax">
              <X aria-hidden="true" />
            </Button>
          </SheetClose>
        </SheetHeader>
        <div className="grid gap-4 px-4">
          <ul className="grid gap-2">
            {syntaxExamples.map((example) => (
              <li key={example}>
                <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-sm">{example}</code>
              </li>
            ))}
          </ul>
          <SheetDescription>
            Queries use Zoekt syntax. Regular expressions are enabled by default. Combine filters with spaces. Prefix a term with - to negate it.
          </SheetDescription>
        </div>
      </SheetContent>
    </Sheet>
  )
}
