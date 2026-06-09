/**
 * Purpose: Root application shell — BrowserRouter, route tree, global keyboard shortcuts.
 * Inputs:  None (ThemeProvider in main.tsx handles theme; RouterApp owns the route tree).
 * Outputs: BrowserRouter wrapping RouterApp + CommandPalette portal.
 * Constraints: BrowserRouter works with Tauri 2 custom protocol (tauri://localhost).
 *   Theme is applied by ThemeProvider (main.tsx) — no manual useEffect here.
 *   CommandPalette is a portal rendered above all routes; ⌘K / Ctrl+K opens it.
 *   BrowserRouter is always rendered so that <Navigate> and useNavigate inside
 *   RouterApp / route guards always have Router context available.
 * SPORT: MASTER-COMPONENTS.md — App
 */

import { useEffect } from 'react'
import { BrowserRouter } from 'react-router-dom'
import { CommandPalette } from './components/CommandPalette'
import { RouterApp } from './routes/index'
import { useCommandPalette } from './hooks/useCommandPalette'
import { useWizardLaunch } from './features/onboarding/useWizardLaunch'
import type { WizardStatus } from './features/onboarding/types'

/**
 * Determines whether the wizard should be launched based on status.
 * Extracted to keep App JSX free of inline type narrowing.
 */
function shouldLaunchWizard(status: WizardStatus | null): boolean {
  if (!status) return false
  return 'NeverRun' in status || 'InProgress' in status
}

export default function App() {
  const { isOpen, open, close } = useCommandPalette()
  const { status, isLoading } = useWizardLaunch()

  // Global keyboard shortcut: ⌘K / Ctrl+K opens command palette.
  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        open()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [open])

  // BrowserRouter is always rendered first so Navigate / useNavigate hooks in
  // child components always have Router context — avoids the "useNavigate must
  // be used within a <Router>" invariant violation.
  return (
    <BrowserRouter>
      {/* Command palette renders as a portal above all routes */}
      <CommandPalette open={isOpen} onClose={close} />
      <RouterApp isLoading={isLoading} launchWizard={shouldLaunchWizard(status)} />
    </BrowserRouter>
  )
}
