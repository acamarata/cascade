/**
 * Purpose: Left-side navigation panel with links to all main routes.
 *   Supports a collapse toggle that hides labels while keeping icon-only nav.
 * Inputs:  None (self-contained; reads location via NavLink internals).
 * Outputs: <nav> landmark with 4 NavItems + a collapse button.
 * Constraints: role="navigation" + aria-label="Main navigation" for accessibility.
 *   Collapsed state is local (useState) — not persisted between sessions in P3.
 *   Width: expanded = w-56, collapsed = w-14.
 * SPORT: MASTER-COMPONENTS.md — Sidebar
 */

import { useRef, useState } from 'react'
import {
  BarChart2,
  BookOpen,
  BookText,
  Brain,
  ClipboardList,
  Database,
  GitFork,
  Inbox,
  KanbanSquare,
  LayoutGrid,
  Library,
  MessageSquare,
  Network,
  PanelLeftClose,
  PanelLeftOpen,
  Search,
  Settings,
  UserCircle2,
  Users,
} from 'lucide-react'
import { useArrowNav } from '../../hooks/useArrowNav'
import { NavItem } from './NavItem'

const NAV_ITEMS = [
  {
    to: '/chat',
    icon: <MessageSquare className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Chat',
  },
  { to: '/inbox', icon: <Inbox className="h-4 w-4 shrink-0" aria-hidden="true" />, label: 'Inbox' },
  {
    to: '/tasks',
    icon: <KanbanSquare className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Tasks',
  },
  {
    to: '/search',
    icon: <Search className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Search',
  },
  {
    to: '/templates',
    icon: <LayoutGrid className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Templates',
  },
  {
    to: '/library',
    icon: <BookOpen className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Library',
  },
  {
    to: '/context',
    icon: <ClipboardList className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Context',
  },
  {
    to: '/project-map',
    icon: <GitFork className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Project Map',
  },
  {
    to: '/pews',
    icon: <Network className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'PEWS',
  },
  {
    to: '/vault',
    icon: <Library className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Vault',
  },
  {
    to: '/vault/memory',
    icon: <Brain className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Memory',
  },
  {
    to: '/vault/personal',
    icon: <UserCircle2 className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Personal Vault',
  },
  {
    to: '/vault/instructions',
    icon: <BookText className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Instructions',
  },
  {
    to: '/rag-explorer',
    icon: <Database className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'RAG Explorer',
  },
  {
    to: '/usage',
    icon: <BarChart2 className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Usage',
  },
  {
    to: '/accounts',
    icon: <Users className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Accounts',
  },
  {
    to: '/settings',
    icon: <Settings className="h-4 w-4 shrink-0" aria-hidden="true" />,
    label: 'Settings',
  },
] as const

export function Sidebar() {
  const [collapsed, setCollapsed] = useState(false)
  const navRef = useRef<HTMLDivElement>(null)
  useArrowNav(navRef)

  return (
    <nav
      role="navigation"
      aria-label="Main navigation"
      className={[
        'flex flex-col border-r border-border bg-card transition-all duration-200',
        collapsed ? 'w-14' : 'w-56',
      ].join(' ')}
    >
      {/* Logo / app name */}
      <div className="flex h-12 items-center gap-2 border-b border-border px-3">
        <span className="h-6 w-6 shrink-0 rounded bg-primary" aria-hidden="true" />
        {!collapsed && <span className="text-sm font-semibold tracking-tight">Cascade</span>}
      </div>

      {/* Nav links */}
      <div ref={navRef} className="flex flex-1 flex-col gap-1 p-2">
        {NAV_ITEMS.map((item) =>
          collapsed ? (
            <a
              key={item.to}
              href={item.to}
              title={item.label}
              className="flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
            >
              {item.icon}
            </a>
          ) : (
            <NavItem key={item.to} to={item.to} icon={item.icon} label={item.label} />
          )
        )}
      </div>

      {/* Collapse toggle */}
      <div className="border-t border-border p-2">
        <button
          type="button"
          onClick={() => setCollapsed((c) => !c)}
          aria-expanded={!collapsed}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          className="flex h-9 w-full items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
        >
          {collapsed ? (
            <PanelLeftOpen className="h-4 w-4" aria-hidden="true" />
          ) : (
            <PanelLeftClose className="h-4 w-4" aria-hidden="true" />
          )}
          {!collapsed && <span className="ml-2 text-xs">Collapse</span>}
        </button>
      </div>
    </nav>
  )
}
