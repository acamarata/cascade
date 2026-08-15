/**
 * Tests for VaultContext fixture-fallback gating (T-P7-E10-04).
 *
 * Covers:
 *   - In a non-Tauri dev environment (Vitest/jsdom), the provider serves the
 *     fixture and exposes `usingFixture: true`.
 *   - The DevFixtureBanner is rendered while fixture data is active.
 *   - `import.meta.env.DEV` is the gate: the fixture is only served when dev
 *     mode is active (verified by the test environment itself, which sets
 *     DEV=true).
 */

import { render, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { ReactNode } from 'react'
import { VaultProvider, useVault } from './VaultContext'

/** Wrapper that mounts VaultProvider with default props. */
function wrapper({ children }: { children: ReactNode }) {
  return (
    <VaultProvider defaultRoot="~/.cascade">{children}</VaultProvider>
  )
}

describe('VaultContext fixture-fallback gating (T-P7-E10-04)', () => {
  it('serves fixture data and sets usingFixture=true in a non-Tauri dev env', async () => {
    // jsdom: window.__TAURI__ is undefined → isTauri() is false.
    // Vitest: import.meta.env.DEV is true → isDev() is true.
    // Both conditions hold → fixture is served.
    const { result } = renderHook(() => useVault(), { wrapper })

    await waitFor(() => {
      expect(result.current.treeData).not.toBeNull()
    })

    expect(result.current.usingFixture).toBe(true)
    expect(result.current.treeData?.name).toBe('.cascade')
  })

  it('renders the DevFixtureBanner while fixture data is active', async () => {
    const { findByTestId } = render(
      <VaultProvider defaultRoot="~/.cascade">
        <div>child</div>
      </VaultProvider>
    )

    const banner = await findByTestId('vault-dev-fixture-banner')
    expect(banner).toBeTruthy()
    expect(banner.getAttribute('role')).toBe('alert')
    expect(banner.textContent).toContain('Dev fixture data')
  })

  it('does not serve fixture content when isDev() is false', async () => {
    // Simulate a production build by stubbing import.meta.env.DEV to false.
    const originalDev = import.meta.env.DEV
    ;(import.meta.env as { DEV: boolean }).DEV = false

    const { result } = renderHook(() => useVault(), { wrapper })

    await waitFor(() => {
      // Without Tauri and without DEV, the provider must error rather than
      // serve fake data.
      expect(result.current.error).not.toBeNull()
    })

    expect(result.current.usingFixture).toBe(false)
    expect(result.current.treeData).toBeNull()
    expect(result.current.error).toContain('Vault IPC unavailable')

    // Restore the original value so other tests are unaffected.
    ;(import.meta.env as { DEV: boolean }).DEV = originalDev
  })
})