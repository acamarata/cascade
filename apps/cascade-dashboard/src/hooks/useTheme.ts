/**
 * Purpose: Manage light/dark theme with localStorage persistence and system preference fallback.
 * Inputs: none — reads localStorage key 'cascade-theme' and window.matchMedia on mount.
 * Outputs: { theme: 'light' | 'dark', toggleTheme: () => void }
 * Constraints: Applies/removes the 'dark' class on document.documentElement for Tailwind class strategy.
 * SPORT: T-P3-E02-05 theme system
 */
import { useState, useEffect } from 'react'

type Theme = 'light' | 'dark'

const STORAGE_KEY = 'cascade-theme'

function getInitialTheme(): Theme {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (stored === 'light' || stored === 'dark') {
    return stored
  }
  // Fall back to system preference
  if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
    return 'dark'
  }
  return 'light'
}

function applyTheme(theme: Theme): void {
  const root = document.documentElement
  if (theme === 'dark') {
    root.classList.add('dark')
  } else {
    root.classList.remove('dark')
  }
}

export function useTheme(): { theme: Theme; toggleTheme: () => void } {
  const [theme, setTheme] = useState<Theme>(() => {
    const initial = getInitialTheme()
    applyTheme(initial)
    return initial
  })

  useEffect(() => {
    applyTheme(theme)
    localStorage.setItem(STORAGE_KEY, theme)
  }, [theme])

  const toggleTheme = () => {
    setTheme(prev => (prev === 'dark' ? 'light' : 'dark'))
  }

  return { theme, toggleTheme }
}
