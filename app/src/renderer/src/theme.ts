// The theme choice: system (the OS picks light or dark), light, dark, or glass.
// ?theme= on the page forces one for screenshots without touching the saved choice.

export type Theme = 'system' | 'light' | 'dark' | 'glass'

export const THEMES: { id: Theme; label: string }[] = [
  { id: 'system', label: 'Match system' },
  { id: 'light', label: 'Light' },
  { id: 'dark', label: 'Dark' },
  { id: 'glass', label: 'Glass' },
]

const key = 'commander.theme'

function isTheme(v: string | null): v is Theme {
  return THEMES.some((t) => t.id === v)
}

export function forcedTheme(): Theme | null {
  const t = new URLSearchParams(location.search).get('theme')
  return isTheme(t) ? t : null
}

export function loadTheme(): Theme {
  const forced = forcedTheme()
  if (forced) return forced
  try {
    const saved = localStorage.getItem(key)
    if (isTheme(saved)) return saved
  } catch {
    // no storage: follow the OS
  }
  return 'system'
}

export function applyTheme(t: Theme): void {
  if (t === 'system') delete document.documentElement.dataset.theme
  else document.documentElement.dataset.theme = t
}

export function saveTheme(t: Theme): void {
  applyTheme(t)
  try {
    localStorage.setItem(key, t)
  } catch {
    // the choice just won't stick
  }
}
