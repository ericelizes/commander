import { useState } from 'react'
import { THEMES, loadTheme, saveTheme, type Theme } from '../theme'

/** ThemeSwitch: system, light, dark or glass, as four small icons. */
export function ThemeSwitch() {
  const [theme, setTheme] = useState(loadTheme)
  const pick = (t: Theme) => {
    setTheme(t)
    saveTheme(t)
  }
  return (
    <div className="theme-switch" role="radiogroup" aria-label="Theme">
      {THEMES.map((t) => (
        <button key={t.id} type="button" role="radio" aria-checked={theme === t.id} aria-label={t.label} title={t.label} className={theme === t.id ? 'on' : ''} onClick={() => pick(t.id)}>
          <ThemeIcon id={t.id} />
        </button>
      ))}
    </div>
  )
}

function ThemeIcon({ id }: { id: Theme }) {
  const p = { width: 14, height: 14, viewBox: '0 0 16 16', fill: 'none', stroke: 'currentColor', strokeWidth: 1.4, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  if (id === 'system')
    return (
      <svg {...p}>
        <circle cx="8" cy="8" r="5.5" />
        <path d="M8 2.5v11a5.5 5.5 0 0 0 0-11z" fill="currentColor" stroke="none" />
      </svg>
    )
  if (id === 'light')
    return (
      <svg {...p}>
        <circle cx="8" cy="8" r="2.8" />
        <path d="M8 1.5v1.6M8 12.9v1.6M1.5 8h1.6M12.9 8h1.6M3.4 3.4l1.1 1.1M11.5 11.5l1.1 1.1M3.4 12.6l1.1-1.1M11.5 4.5l1.1-1.1" />
      </svg>
    )
  if (id === 'dark')
    return (
      <svg {...p}>
        <path d="M13.2 9.6A5.6 5.6 0 0 1 6.4 2.8a5.6 5.6 0 1 0 6.8 6.8z" />
      </svg>
    )
  return (
    <svg {...p}>
      <rect x="2.5" y="4.5" width="8" height="8" rx="2" />
      <rect x="5.5" y="2.5" width="8" height="8" rx="2" fill="currentColor" fillOpacity="0.28" />
    </svg>
  )
}
