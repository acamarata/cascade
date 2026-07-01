/**
 * Purpose: Typed command registry — allows any feature to register actions at startup.
 * Inputs:  CommandAction objects with id, label, keywords, and onExecute callback.
 * Outputs: Iterable action list for the command palette to render and filter.
 * Constraints: Module-level singleton (globalRegistry) — safe for this single-app scope;
 *   no circular imports since registry has zero app dependencies.
 * SPORT: MASTER-LIBS.md — CommandRegistry, src/lib/commands/registry.ts
 */

import type { NavigateFunction } from 'react-router-dom'
import type { ThemeToken } from '../../store/theme.slice'

export interface CommandAction {
  id: string
  label: string
  group?: string
  keywords?: string[]
  shortcut?: string
  onExecute: () => void
}

export class CommandRegistry {
  private actions: CommandAction[] = []

  register(action: CommandAction): void {
    // Replace if id already registered (idempotent re-registration)
    const idx = this.actions.findIndex((a) => a.id === action.id)
    if (idx >= 0) {
      this.actions[idx] = action
    } else {
      this.actions.push(action)
    }
  }

  unregister(id: string): void {
    this.actions = this.actions.filter((a) => a.id !== id)
  }

  getAll(): CommandAction[] {
    return [...this.actions]
  }
}

/** Module-level singleton — import and use directly anywhere in the app. */
export const globalRegistry = new CommandRegistry()

/**
 * Purpose: Build the initial set of app-wide commands (navigation + theme).
 * Inputs:  navigate function from react-router, setTheme from theme store.
 * Outputs: 8 CommandAction objects registered into globalRegistry.
 * Constraints: Call once in AppLayout effect; re-calls are idempotent.
 */
export function registerDefaultCommands(
  navigate: NavigateFunction,
  setTheme: (token: ThemeToken) => void
): void {
  const navCommands: CommandAction[] = [
    {
      id: 'nav:chat',
      label: 'Go to Chat',
      group: 'Navigate',
      keywords: ['home', 'main', 'dashboard'],
      onExecute: () => navigate('/chat'),
    },
    {
      id: 'nav:inbox',
      label: 'Go to Inbox',
      group: 'Navigate',
      keywords: ['messages', 'notifications'],
      onExecute: () => navigate('/inbox'),
    },
    {
      id: 'nav:search',
      label: 'Go to Search',
      group: 'Navigate',
      keywords: ['find', 'query'],
      onExecute: () => navigate('/search'),
    },
    {
      id: 'nav:settings',
      label: 'Go to Settings',
      group: 'Navigate',
      keywords: ['preferences', 'config'],
      onExecute: () => navigate('/settings'),
    },
  ]

  const themeCommands: CommandAction[] = [
    {
      id: 'theme:light',
      label: 'Set theme: Light',
      group: 'Theme',
      keywords: ['appearance', 'bright'],
      onExecute: () => setTheme('light'),
    },
    {
      id: 'theme:dark',
      label: 'Set theme: Dark',
      group: 'Theme',
      keywords: ['appearance', 'night'],
      onExecute: () => setTheme('dark'),
    },
    {
      id: 'theme:system',
      label: 'Set theme: System',
      group: 'Theme',
      keywords: ['appearance', 'auto'],
      onExecute: () => setTheme('system'),
    },
  ]

  ;[...navCommands, ...themeCommands].forEach((cmd) => globalRegistry.register(cmd))
}
