/**
 * Purpose: Applies the resolved Tailwind dark-mode class to <html> whenever the
 *   theme slice's resolvedTheme changes. Renders children with no extra DOM nodes.
 * Inputs:  children — React nodes to render.
 * Outputs: Mounts/unmounts the "dark" class on document.documentElement as a side-effect.
 * Constraints: Must wrap RouterApp so that dark class is present before first paint.
 *   Uses useEffect — never mutates the DOM during render.
 * SPORT: MASTER-COMPONENTS.md — ThemeProvider
 */

import { useEffect } from 'react'
import { useThemeStore } from '../store/index'

interface ThemeProviderProps {
  children: React.ReactNode
}

export function ThemeProvider({ children }: ThemeProviderProps) {
  const { resolvedTheme } = useThemeStore()

  useEffect(() => {
    const root = document.documentElement
    if (resolvedTheme === 'dark') {
      root.classList.add('dark')
    } else {
      root.classList.remove('dark')
    }
  }, [resolvedTheme])

  return <>{children}</>
}
