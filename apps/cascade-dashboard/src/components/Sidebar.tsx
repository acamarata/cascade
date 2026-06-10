/**
 * Purpose: Left sidebar navigation with three top-level sections.
 * Inputs: none — uses react-router-dom NavLink for active state.
 * Outputs: JSX sidebar element with nav links and aria-current support.
 * Constraints: aria-current="page" on active link for WCAG 2.1 AA; keyboard tab navigation.
 * SPORT: T-P3-E02-04 layout shell
 */
import { NavLink } from 'react-router-dom'

interface NavItem {
  to: string
  label: string
}

const NAV_ITEMS: NavItem[] = [
  { to: '/global', label: 'Global' },
  { to: '/personal', label: 'Personal' },
  { to: '/projects', label: 'Projects' },
]

export function Sidebar() {
  return (
    <aside
      className="flex flex-col w-[var(--sidebar-width)] shrink-0 border-r border-border bg-card h-full"
      aria-label="Main navigation"
    >
      <div className="px-4 py-5 border-b border-border">
        <span className="text-sm font-semibold tracking-wide text-foreground">
          cascade
        </span>
      </div>

      <nav className="flex-1 px-2 py-4 overflow-y-auto">
        <ul className="space-y-1" role="list">
          {NAV_ITEMS.map(({ to, label }) => (
            <li key={to}>
              <NavLink
                to={to}
                className={({ isActive }) =>
                  [
                    'flex items-center px-3 py-2 rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                    isActive
                      ? 'bg-zinc-800 text-white dark:bg-zinc-700'
                      : 'text-muted-foreground hover:bg-accent hover:text-accent-foreground',
                  ].join(' ')
                }
              >
                {({ isActive }) => (
                  <span aria-current={isActive ? 'page' : undefined}>
                    {label}
                  </span>
                )}
              </NavLink>
            </li>
          ))}
        </ul>
      </nav>
    </aside>
  )
}
