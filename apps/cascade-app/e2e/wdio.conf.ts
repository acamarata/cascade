/**
 * Purpose: WebdriverIO configuration for Tauri app E2E tests.
 *   Targets the compiled Cascade Tauri binary via tauri-driver.
 *   Requires: built binary (`pnpm tauri:build`) + display server (Xvfb on Linux).
 *
 * NOTE: This config is NOT run in headless CI. It requires:
 *   - macOS: native display (run `pnpm test:e2e:wdio` locally).
 *   - Linux CI: add `Xvfb :99 -screen 0 1280x768x24` before invocation.
 *   The `wizard.e2e.ts` spec is annotated with @ci-with-display to signal this.
 *
 * Run locally:
 *   pnpm --dir apps/cascade-app tauri:build
 *   pnpm --dir apps/cascade-app test:e2e:wdio
 *
 * SPORT: MASTER-COMPONENTS.md — wizard E2E test suite — e2e/wdio.conf.ts
 * Task: T-P3-E03-34
 */

import type { Options } from '@wdio/types'
import path from 'path'

// Path to the compiled Tauri binary (macOS app bundle)
const TAURI_BINARY =
  process.env.TAURI_BINARY ??
  path.resolve(
    __dirname,
    '../../src-tauri/target/release/bundle/macos/cascade-app.app/Contents/MacOS/cascade-app'
  )

export const config: Options.Testrunner = {
  runner: 'local',

  // Spec pattern: only the e2e directory
  specs: ['./e2e/*.e2e.ts'],

  // Use tauri-driver as the WebDriver server
  // Install: cargo install tauri-driver
  // See: https://tauri.app/v1/guides/testing/webdriver/introduction
  services: [
    [
      'tauri',
      {
        // tauri-driver binary (assumes `cargo install tauri-driver` ran)
        tauriDriverPath: process.env.TAURI_DRIVER_BIN ?? 'tauri-driver',
      },
    ],
  ],

  capabilities: [
    {
      // The tauri WebdriverIO capability
      'tauri:options': {
        application: TAURI_BINARY,
      },
      browserName: 'wry',
    },
  ],

  // Test framework
  framework: 'mocha',
  mochaOpts: {
    ui: 'bdd',
    timeout: 60_000,
  },

  // Reporter
  reporters: ['spec'],

  // Parallelism: 1 (Tauri app can only run one instance during test)
  maxInstances: 1,

  // Log level
  logLevel: 'info',

  // Bail on first failure during CI to save time
  bail: process.env.CI ? 1 : 0,

  // Base URL not needed for Tauri apps (no HTTP server)
  baseUrl: '',
}
