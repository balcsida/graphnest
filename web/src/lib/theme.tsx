import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'

export type Theme = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'graphnest-theme'
const order: Theme[] = ['light', 'dark', 'system']

function readTheme(): Theme {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    return order.find((theme) => theme === stored) ?? 'system'
  } catch {
    return 'system'
  }
}

const prefersDark = () => window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false

function applyTheme(theme: Theme) {
  const dark = theme === 'dark' || (theme === 'system' && prefersDark())
  document.documentElement.classList.toggle('dark', dark)
}

interface ThemeContextValue {
  theme: Theme
  setTheme: (theme: Theme) => void
  /** Cycles light, dark, system. */
  cycleTheme: () => void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(readTheme)

  useEffect(() => {
    applyTheme(theme)
    if (theme !== 'system' || !window.matchMedia) return
    const query = window.matchMedia('(prefers-color-scheme: dark)')
    const listener = () => applyTheme('system')
    query.addEventListener('change', listener)
    return () => query.removeEventListener('change', listener)
  }, [theme])

  const setTheme = useCallback((next: Theme) => {
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // Preference is then kept for this page load only.
    }
    setThemeState(next)
  }, [])

  const cycleTheme = useCallback(() => setTheme(order[(order.indexOf(theme) + 1) % order.length]), [theme, setTheme])

  const value = useMemo(() => ({ theme, setTheme, cycleTheme }), [theme, setTheme, cycleTheme])
  return <ThemeContext value={value}>{children}</ThemeContext>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useTheme(): ThemeContextValue {
  const value = useContext(ThemeContext)
  if (!value) throw new Error('useTheme must be used inside ThemeProvider')
  return value
}
