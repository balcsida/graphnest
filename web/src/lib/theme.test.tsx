import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { ThemeToggle } from '@/components/ThemeToggle'
import { ThemeProvider } from '@/lib/theme'

const mount = () =>
  render(
    <ThemeProvider>
      <ThemeToggle />
    </ThemeProvider>,
  )

describe('theme toggle', () => {
  it('defaults to system and cycles light, dark, system', async () => {
    const user = userEvent.setup()
    mount()
    const toggle = () => screen.getByRole('button', { name: /switch theme/i })
    expect(toggle()).toHaveAccessibleName('Theme: system. Switch theme')

    await user.click(toggle())
    expect(toggle()).toHaveAccessibleName('Theme: light. Switch theme')
    expect(localStorage.getItem('graphnest-theme')).toBe('light')
    expect(document.documentElement).not.toHaveClass('dark')

    await user.click(toggle())
    expect(localStorage.getItem('graphnest-theme')).toBe('dark')
    expect(document.documentElement).toHaveClass('dark')

    await user.click(toggle())
    expect(localStorage.getItem('graphnest-theme')).toBe('system')
    expect(document.documentElement).not.toHaveClass('dark')
  })

  it('restores the stored preference', () => {
    localStorage.setItem('graphnest-theme', 'dark')
    mount()
    expect(document.documentElement).toHaveClass('dark')
  })
})
