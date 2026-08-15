/**
 * Purpose: Dev-only vault fixture data + the dev-mode banner shown when
 *   fixture data is being served instead of real Tauri IPC. Extracted from
 *   VaultContext so the provider stays under the 300-line file cap.
 * Inputs:  none (static fixture).
 * Outputs: VAULT_FIXTURE — hard-coded VaultNode tree used when BOTH
 *            (a) the Tauri runtime is absent AND (b) import.meta.env.DEV is true.
 *          DevFixtureBanner — visible banner rendered while fixture data is active.
 * Constraints: Fixture data must never ship to a production webview; the
 *   provider gates on `!isTauri() && import.meta.env.DEV` before using it.
 * SPORT: MASTER-COMPONENTS.md — VaultContext (T-P3-E06-02)
 */

import { AlertTriangle } from 'lucide-react'
import type { VaultNode } from '../types/vault'

// ── Mock IPC fixture ─────────────────────────────────────────────────────────

/**
 * Hard-coded tree returned when the Tauri runtime is absent AND the app is
 * running in a Vite dev build (`import.meta.env.DEV`). Used by Vitest /
 * Storybook / browser dev. Never served in a production webview.
 */
export const VAULT_FIXTURE: VaultNode = {
  name: '.cascade',
  path: '~/.cascade',
  isDir: true,
  children: [
    {
      name: 'GCI',
      path: '~/.cascade/GCI',
      isDir: true,
      children: [
        {
          name: 'CLAUDE.md',
          path: '~/.cascade/GCI/CLAUDE.md',
          isDir: false,
          children: [],
        },
        {
          name: 'rules.md',
          path: '~/.cascade/GCI/rules.md',
          isDir: false,
          children: [],
        },
      ],
    },
    {
      name: 'PCI',
      path: '~/.cascade/PCI',
      isDir: true,
      children: [
        {
          name: 'project.md',
          path: '~/.cascade/PCI/project.md',
          isDir: false,
          children: [],
        },
      ],
    },
    {
      name: 'APC',
      path: '~/.cascade/APC',
      isDir: true,
      children: [],
    },
    {
      name: 'PPC',
      path: '~/.cascade/PPC',
      isDir: true,
      children: [],
    },
    {
      name: 'PRC',
      path: '~/.cascade/PRC',
      isDir: true,
      children: [
        {
          name: 'repo.md',
          path: '~/.cascade/PRC/repo.md',
          isDir: false,
          children: [],
        },
      ],
    },
    {
      name: 'PAC',
      path: '~/.cascade/PAC',
      isDir: true,
      children: [],
    },
  ],
}

// ── Dev-mode banner ──────────────────────────────────────────────────────────

/**
 * Visible banner rendered whenever the VaultProvider is serving fixture data
 * instead of real Tauri IPC. Ensures a dev-only data source can never be
 * confused with real vault contents.
 *
 * Inputs:  none (presentational).
 * Outputs: a fixed-position amber banner at the top of the viewport.
 * Constraints: only mounted by VaultProvider when `usingFixture` is true.
 */
export function DevFixtureBanner() {
  return (
    <div
      role="alert"
      aria-live="polite"
      data-testid="vault-dev-fixture-banner"
      className="pointer-events-none fixed inset-x-0 top-0 z-[9999] flex items-center justify-center gap-2 border-b border-amber-500/40 bg-amber-500/15 px-4 py-1.5 text-[11px] font-medium text-amber-200"
    >
      <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span>
        Dev fixture data — vault IPC unavailable. Showing mock vault, not real
        files.
      </span>
    </div>
  )
}