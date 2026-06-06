/**
 * Purpose: Icon button that cycles theme token dark → light → system on each click.
 * Inputs:  None (reads/writes useThemeStore).
 * Outputs: <button> with a Sun, Moon, or Monitor icon matching the current token.
 * Constraints: aria-label="Toggle theme" and title attribute for keyboard/screen-reader access.
 *   Must not import from src/components/ui/ unless it's an icon — use a plain <button>.
 * SPORT: MASTER-COMPONENTS.md — ThemeToggle
 */

import { Monitor, Moon, Sun } from 'lucide-react';
import { useThemeStore } from '../store/index';
import type { ThemeToken } from '../store/theme.slice';

const CYCLE: Record<ThemeToken, ThemeToken> = {
  dark: 'light',
  light: 'system',
  system: 'dark',
};

const ICON_MAP: Record<ThemeToken, React.ReactNode> = {
  dark: <Moon className="h-4 w-4" aria-hidden="true" />,
  light: <Sun className="h-4 w-4" aria-hidden="true" />,
  system: <Monitor className="h-4 w-4" aria-hidden="true" />,
};

const LABEL_MAP: Record<ThemeToken, string> = {
  dark: 'Dark theme — click to switch to light',
  light: 'Light theme — click to switch to system',
  system: 'System theme — click to switch to dark',
};

export function ThemeToggle() {
  const { token, setTheme } = useThemeStore();

  function handleClick() {
    setTheme(CYCLE[token]);
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      aria-label="Toggle theme"
      title={LABEL_MAP[token]}
      className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      {ICON_MAP[token]}
    </button>
  );
}
