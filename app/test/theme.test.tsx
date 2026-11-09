// Theme switch: picking a theme sets <html data-theme> and survives a reload;
// "Match system" clears it so the OS decides; ?theme= forces one without saving.
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { ThemeSwitch } from '../src/renderer/src/components/ThemeSwitch'
import { loadTheme } from '../src/renderer/src/theme'

afterEach(() => {
  cleanup()
  localStorage.clear()
  delete document.documentElement.dataset.theme
  history.replaceState(null, '', '/')
})

describe('theme switch', () => {
  it('applies and remembers the picked theme', async () => {
    const user = userEvent.setup()
    render(<ThemeSwitch />)
    expect(screen.getByRole('radio', { name: 'Match system' })).toHaveAttribute('aria-checked', 'true')

    await user.click(screen.getByRole('radio', { name: 'Glass' }))
    expect(document.documentElement.dataset.theme).toBe('glass')
    expect(loadTheme()).toBe('glass')

    cleanup()
    render(<ThemeSwitch />)
    expect(screen.getByRole('radio', { name: 'Glass' })).toHaveAttribute('aria-checked', 'true')

    await user.click(screen.getByRole('radio', { name: 'Match system' }))
    expect(document.documentElement.dataset.theme).toBeUndefined()
    expect(loadTheme()).toBe('system')
  })

  it('lets ?theme= force a theme over the saved one', () => {
    localStorage.setItem('commander.theme', 'dark')
    history.replaceState(null, '', '/?theme=light')
    expect(loadTheme()).toBe('light')
    history.replaceState(null, '', '/?theme=nope')
    expect(loadTheme()).toBe('dark')
  })
})
