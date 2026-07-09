/**
 * Purpose: Application shell layout — sidebar navigation, top bar, content area.
 * Inputs:  onOpenPalette callback (triggered by top bar ⌘K button)
 * Outputs: <nav> + <main> with <Outlet> for nested routes
 * Constraints:
 *   - All nav links must be keyboard-accessible (Tab + Enter).
 *   - ARIA role="navigation" on <nav>; current page indicated via aria-current.
 *   - Focus rings must be visible (not suppressed globally).
 * SPORT: MASTER-COMPONENTS.md — AppLayout
 */

import { NavLink, Outlet } from 'react-router-dom'
import { Activity, FileText, Inbox, Search, Settings, Zap } from 'lucide-react'
import { DaemonStatusBadge } from './DaemonStatusBadge'

interface AppLayoutProps {
  onOpenPalette: () => void
}

const navItems = [
  { to: '/', label: 'Editor', icon: FileText, shortcut: '⌘1' },
  { to: '/status', label: 'Status', icon: Activity, shortcut: '⌘2' },
  { to: '/inbox', label: 'Inbox', icon: Inbox, shortcut: '⌘3' },
  { to: '/rag', label: 'RAG', icon: Search, shortcut: '⌘4' },
  { to: '/settings', label: 'Settings', icon: Settings, shortcut: '⌘5' },
] as const

export function AppLayout({ onOpenPalette }: AppLayoutProps) {
  return (
    <div className="flex h-screen flex-col bg-background text-foreground">
      {/* Top bar */}
      <header className="flex h-12 shrink-0 items-center justify-between border-b border-border/50 bg-background/80 backdrop-blur-md px-4 shadow-sm">
        <div className="flex items-center gap-2">
          <Zap className="h-4 w-4 text-primary shadow-[0_0_8px_rgba(16,185,129,0.3)]" aria-hidden="true" />
          <span className="text-sm font-bold tracking-tight">Cascade</span>
        </div>

        <button
          type="button"
          onClick={onOpenPalette}
          className="flex items-center gap-2 rounded-lg border border-border/60 bg-muted/40 px-3 py-1.5 text-xs text-muted-foreground hover:bg-accent hover:text-foreground transition-all duration-200 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 w-64 justify-between shadow-sm"
          aria-label="Open command palette (⌘K)"
        >
          <span className="flex items-center gap-2">
            <Search className="h-3.5 w-3.5 text-muted-foreground/80" aria-hidden="true" />
            <span>Search or run command…</span>
          </span>
          <kbd className="rounded border border-border/80 bg-background px-1.5 py-0.5 font-mono text-[9px] font-bold text-muted-foreground/80 shadow-sm">
            ⌘K
          </kbd>
        </button>

        <DaemonStatusBadge />
      </header>

      <div className="flex flex-1 overflow-hidden">
        {/* Sidebar */}
        <nav
          role="navigation"
          aria-label="Main navigation"
          className="flex w-16 flex-col items-center gap-2 border-r border-border/50 bg-muted/10 py-4"
        >
          {navItems.map(({ to, label, icon: Icon, shortcut }) => (
            <NavLink
              key={to}
              to={to}
              end={to === '/'}
              className={({ isActive }) =>
                [
                  'group relative flex h-10 w-10 items-center justify-center rounded-xl transition-all duration-200',
                  'hover:bg-accent/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40',
                  isActive ? 'bg-primary/10 text-primary shadow-sm border border-primary/20' : 'text-muted-foreground hover:text-foreground',
                ].join(' ')
              }
              aria-label={`${label} (${shortcut})`}
            >
              {({ isActive }) => (
                <>
                  <Icon className="h-5 w-5" aria-hidden="true" />
                  {isActive && (
                    <span
                      className="absolute left-0 h-6 w-1 rounded-r-full bg-primary shadow-[0_0_8px_rgba(16,185,129,0.4)]"
                      aria-hidden="true"
                    />
                  )}
                  {/* Tooltip */}
                  <span className="pointer-events-none absolute left-16 z-50 hidden whitespace-nowrap rounded-lg border border-border/60 bg-popover px-2.5 py-1.5 text-xs font-semibold shadow-md group-hover:flex items-center gap-2 animate-in fade-in slide-in-from-left-1 duration-150">
                    {label}
                    <kbd className="text-[10px] font-bold text-muted-foreground/80 bg-muted px-1 py-0.5 rounded border border-border/60">{shortcut}</kbd>
                  </span>
                </>
              )}
            </NavLink>
          ))}
        </nav>

        {/* Main content area */}
        <main className="flex-1 overflow-auto p-4">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
