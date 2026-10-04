import { Monitor, Moon, Sun } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useTheme, type Theme } from '@/lib/theme'

const icons = { light: Sun, dark: Moon, system: Monitor } satisfies Record<Theme, unknown>

export function ThemeToggle() {
  const { theme, cycleTheme } = useTheme()
  const Icon = icons[theme]
  return (
    <Button variant="ghost" size="icon" onClick={cycleTheme} aria-label={`Theme: ${theme}. Switch theme`}>
      <Icon />
    </Button>
  )
}
