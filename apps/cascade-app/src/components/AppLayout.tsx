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

import { NavLink, Outlet } from "react-router-dom";
import {
  Activity,
  FileText,
  Inbox,
  Search,
  Settings,
  Zap,
} from "lucide-react";
import { DaemonStatusBadge } from "./DaemonStatusBadge";

interface AppLayoutProps {
  onOpenPalette: () => void;
}

const navItems = [
  { to: "/",        label: "Editor",   icon: FileText,  shortcut: "⌘1" },
  { to: "/status",  label: "Status",   icon: Activity,  shortcut: "⌘2" },
  { to: "/inbox",   label: "Inbox",    icon: Inbox,     shortcut: "⌘3" },
  { to: "/rag",     label: "RAG",      icon: Search,    shortcut: "⌘4" },
  { to: "/settings",label: "Settings", icon: Settings,  shortcut: "⌘5" },
] as const;

export function AppLayout({ onOpenPalette }: AppLayoutProps) {
  return (
    <div className="flex h-screen flex-col bg-background text-foreground">
      {/* Top bar */}
      <header className="flex h-10 shrink-0 items-center justify-between border-b border-border px-4">
        <div className="flex items-center gap-2">
          <Zap className="h-4 w-4 text-primary" aria-hidden="true" />
          <span className="text-sm font-semibold tracking-tight">Cascade</span>
        </div>

        <button
          type="button"
          onClick={onOpenPalette}
          className="flex items-center gap-1.5 rounded-md border border-border bg-muted px-2.5 py-1 text-xs text-muted-foreground hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label="Open command palette (⌘K)"
        >
          <Search className="h-3.5 w-3.5" aria-hidden="true" />
          <span>Command palette</span>
          <kbd className="ml-1 rounded border border-border bg-background px-1 py-0.5 font-mono text-[10px]">
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
          className="flex w-14 flex-col items-center gap-1 border-r border-border py-3"
        >
          {navItems.map(({ to, label, icon: Icon, shortcut }) => (
            <NavLink
              key={to}
              to={to}
              end={to === "/"}
              className={({ isActive }) =>
                [
                  "group relative flex h-10 w-10 items-center justify-center rounded-md transition-colors",
                  "hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  isActive
                    ? "bg-accent text-accent-foreground"
                    : "text-muted-foreground",
                ].join(" ")
              }
              aria-label={`${label} (${shortcut})`}
            >
              {({ isActive }) => (
                <>
                  <Icon className="h-5 w-5" aria-hidden="true" />
                  {isActive && (
                    <span
                      className="absolute left-0 h-5 w-0.5 rounded-r-full bg-primary"
                      aria-hidden="true"
                    />
                  )}
                  {/* Tooltip */}
                  <span className="pointer-events-none absolute left-14 z-50 hidden whitespace-nowrap rounded-md border border-border bg-popover px-2 py-1 text-xs shadow-md group-hover:flex">
                    {label}
                    <kbd className="ml-2 text-muted-foreground">{shortcut}</kbd>
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
  );
}
