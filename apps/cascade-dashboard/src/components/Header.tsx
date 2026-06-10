/**
 * Purpose: Top header bar with app title and dark/light theme toggle.
 * Inputs: none — consumes useTheme hook internally.
 * Outputs: JSX header element with title text and a sun/moon toggle button.
 * Constraints: Theme button requires aria-label for screen readers; icon is decorative.
 * SPORT: T-P3-E02-04 layout shell, T-P3-E02-05 theme system
 */
import { useTheme } from '@/hooks/useTheme'

function SunIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" />
    </svg>
  )
}

function MoonIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
    </svg>
  )
}

export function Header() {
  const { theme, toggleTheme } = useTheme()

  return (
    <header className="flex items-center justify-between h-12 px-4 border-b border-border bg-card shrink-0">
      <span className="text-sm font-medium text-foreground">
        Cascade Dashboard
      </span>

      <button
        type="button"
        onClick={toggleTheme}
        className="flex items-center justify-center w-8 h-8 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        aria-label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
      >
        {theme === 'dark' ? <SunIcon /> : <MoonIcon />}
      </button>
    </header>
  )
}
