/**
 * Purpose: Single navigation link in the Sidebar using React Router NavLink.
 *   Applies active and hover styles based on the current route.
 *   Exposes aria-current="page" on the active link for assistive technologies.
 * Inputs:  to — destination path; icon — lucide-react element; label — display text.
 * Outputs: <NavLink> styled as a sidebar item with WCAG-compliant focus ring and
 *   aria-current="page" when the route matches.
 * Constraints: aria-current is computed via useMatch outside NavLink so it can be
 *   passed as a static prop (NavLink does not accept function callbacks for aria-*).
 *   Icon must be wrapped with aria-hidden="true" (done inline).
 * SPORT: MASTER-COMPONENTS.md — NavItem
 */

import { NavLink, useMatch, useResolvedPath } from 'react-router-dom';

interface NavItemProps {
  to: string;
  icon: React.ReactNode;
  label: string;
}

export function NavItem({ to, icon, label }: NavItemProps) {
  // Resolve the target path relative to current base, then test if it matches.
  // This mirrors what NavLink does internally for its isActive check.
  const resolved = useResolvedPath(to);
  const match = useMatch({ path: resolved.pathname, end: true });

  return (
    <NavLink
      to={to}
      aria-current={match ? 'page' : undefined}
      className={({ isActive }) =>
        [
          'flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
          isActive
            ? 'bg-accent text-accent-foreground'
            : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
        ].join(' ')
      }
    >
      <span aria-hidden="true">{icon}</span>
      <span>{label}</span>
    </NavLink>
  );
}
