/**
 * Purpose: Root application shell — BrowserRouter, route tree, global keyboard shortcuts.
 * Inputs:  None (ThemeProvider in main.tsx handles theme; RouterApp owns the route tree).
 * Outputs: BrowserRouter wrapping AppInner + RouterApp.
 * Constraints: BrowserRouter works with Tauri 2 custom protocol (tauri://localhost).
 *   Theme is applied by ThemeProvider (main.tsx) — no manual useEffect here.
 *   CommandPalette is a portal rendered above all routes; ⌘K / Ctrl+K opens it.
 *   BrowserRouter is always rendered so that <Navigate> and useNavigate inside
 *   RouterApp / route guards always have Router context available.
 *   AppInner lives inside BrowserRouter so it can call useNavigate (Seam B, T-P3-E06-13).
 * SPORT: MASTER-COMPONENTS.md — App
 */

import { useCallback, useEffect } from 'react'
import { BrowserRouter, useNavigate } from 'react-router-dom'
import { CommandPalette } from './components/CommandPalette'
import { SearchPanel } from './components/vault/SearchPanel'
import { RouterApp } from './routes/index'
import { useCommandPalette } from './hooks/useCommandPalette'
import { useSearchPanel } from './hooks/useSearchPanel'
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

// ── AppInner — inside BrowserRouter, can call useNavigate ─────────────────────

/**
 * Inner shell rendered inside BrowserRouter.
 * Owns SearchPanel + CommandPalette so they can use useNavigate (Seam B, T-P3-E06-13).
 */
function AppInner() {
  const navigate = useNavigate()
  const { isOpen, open, close } = useCommandPalette()
  const { isOpen: searchOpen, close: searchClose } = useSearchPanel()
  const { status, isLoading } = useWizardLaunch()

  // Vault root — resolved from the default cascade home.
  // A real implementation would read this from AppState / user config (T-P3-E06-later).
  const vaultRoot =
    typeof window !== 'undefined'
      ? (window as typeof window & { __CASCADE_VAULT_ROOT?: string }).__CASCADE_VAULT_ROOT
      : undefined

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

  /**
   * Seam B (T-P3-E06-13): SearchPanel/MemoryCard onOpenNote → navigate to /vault.
   * The /vault route mounts VaultProvider; setCurrentFile is called via a URL param
   * so VaultPageInner picks up the file on mount.
   *
   * Implementation: navigate to /vault with ?file=<encoded-path>; VaultPage reads
   * the param and calls setCurrentFile(file) in a useEffect. This avoids needing
   * an external singleton store or a context that spans multiple route trees.
   *
   * The scrollToLine Tauri event is emitted inside SearchPanel.openResult and
   * MarkdownEditor listens for it — that path is unchanged.
   */
  const handleOpenNote = useCallback(
    (path: string, _lineNo?: number) => {
      navigate(`/vault?file=${encodeURIComponent(path)}`)
    },
    [navigate]
  )

  return (
    <>
      {/* Command palette renders as a portal above all routes */}
      <CommandPalette open={isOpen} onClose={close} />
      {/* Vault search panel — toggled by Cmd+Shift+F / Ctrl+Shift+F */}
      <SearchPanel
        root={vaultRoot}
        open={searchOpen}
        onClose={searchClose}
        onOpenNote={handleOpenNote}
      />
      <RouterApp isLoading={isLoading} launchWizard={shouldLaunchWizard(status)} />
    </>
  )
}

// ── App — BrowserRouter wrapper ────────────────────────────────────────────────

export default function App() {
  // BrowserRouter is always rendered first so Navigate / useNavigate hooks in
  // child components always have Router context — avoids the "useNavigate must
  // be used within a <Router>" invariant violation.
  return (
    <BrowserRouter>
      <AppInner />
    </BrowserRouter>
  )
}
