// @vitest-environment jsdom
/**
 * Purpose: Unit tests for settings panel helpers and tab smoke tests.
 *   Tests: cron validation, env/args parsing, isSectionDirty, tab renders.
 * Inputs:  None — pure unit tests; no real IPC calls (Tauri mocked).
 * Constraints: No real Tauri context required. Vitest jsdom environment.
 * SPORT: MASTER-COMPONENTS.md — settings tests — T-P3-E07-15/16
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import '@testing-library/jest-dom'

// ---------------------------------------------------------------------------
// Mocks — must come before any import that touches @tauri-apps/api/core
// ---------------------------------------------------------------------------

vi.mock('@tauri-apps/api/core', () => ({
  invoke: vi.fn().mockResolvedValue({
    schemaVersion: '2',
    library: { defaultHarnessTargets: [] },
    context: {},
    memory: { personalChatSync: false },
    projectMap: {},
    providers: { google: [] },
    geminiPool: { keys: [], proxyPort: 3761, enabled: false },
    harnessBridges: {},
    hooks: [],
    scheduledTasks: [],
    plugins: { enabled: [], config: {} },
    widgets: { positions: {} },
    mcpServers: [],
    vaultDisplay: { showMasked: false },
    telemetry: { enabled: false },
  }),
}))

vi.mock('@tauri-apps/plugin-dialog', () => ({
  open: vi.fn().mockResolvedValue(null),
}))

vi.mock('@tauri-apps/plugin-opener', () => ({
  open: vi.fn().mockResolvedValue(undefined),
}))

// ---------------------------------------------------------------------------
// Imports (after mocks)
// ---------------------------------------------------------------------------

import { isValidCron } from './ScheduledTasksTab'
import { envToText, textToEnv, argsToText, textToArgs } from './McpServersTab'
import { isSectionDirty, defaultSettings } from './useSettings'
import { HOOK_EVENTS } from './HooksTab'

// ---------------------------------------------------------------------------
// isValidCron
// ---------------------------------------------------------------------------

describe('isValidCron', () => {
  it('accepts standard 5-field expression', () => {
    expect(isValidCron('0 2 * * *')).toBe(true)
  })

  it('accepts 6-field expression', () => {
    expect(isValidCron('0 0 2 * * *')).toBe(true)
  })

  it('accepts step values', () => {
    expect(isValidCron('*/5 * * * *')).toBe(true)
  })

  it('rejects alphabetic fields', () => {
    expect(isValidCron('foo * * * *')).toBe(false)
  })

  it('rejects too few fields', () => {
    expect(isValidCron('* * * *')).toBe(false)
  })

  it('rejects too many fields', () => {
    expect(isValidCron('0 0 * * * * *')).toBe(false)
  })

  it('rejects empty string', () => {
    expect(isValidCron('')).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// envToText / textToEnv round-trip
// ---------------------------------------------------------------------------

describe('envToText / textToEnv', () => {
  it('round-trips a simple env record', () => {
    const env = { HOME: '/root', NODE_ENV: 'production' }
    const text = envToText(env)
    expect(textToEnv(text)).toEqual(env)
  })

  it('handles values containing "="', () => {
    const env = { URL: 'http://example.com?a=1&b=2' }
    expect(textToEnv(envToText(env))).toEqual(env)
  })

  it('skips lines without "="', () => {
    expect(textToEnv('VALID=ok\nnot-a-pair\nALSO=yes')).toEqual({ VALID: 'ok', ALSO: 'yes' })
  })

  it('produces empty object from empty string', () => {
    expect(textToEnv('')).toEqual({})
  })
})

// ---------------------------------------------------------------------------
// argsToText / textToArgs round-trip
// ---------------------------------------------------------------------------

describe('argsToText / textToArgs', () => {
  it('round-trips an args array', () => {
    const args = ['-y', '@modelcontextprotocol/server-filesystem', '/tmp']
    expect(textToArgs(argsToText(args))).toEqual(args)
  })

  it('filters empty segments', () => {
    expect(textToArgs(',foo,,bar,')).toEqual(['foo', 'bar'])
  })

  it('trims whitespace', () => {
    expect(textToArgs('  a ,  b  , c  ')).toEqual(['a', 'b', 'c'])
  })
})

// ---------------------------------------------------------------------------
// isSectionDirty
// ---------------------------------------------------------------------------

describe('isSectionDirty', () => {
  const base = defaultSettings()

  it('returns false when section unchanged', () => {
    expect(isSectionDirty(base, base, 'providers')).toBe(false)
  })

  it('returns true when providers section mutated', () => {
    const mutated = { ...base, providers: { ...base.providers, anthropic: 'sk-ant-test' } }
    expect(isSectionDirty(base, mutated, 'providers')).toBe(true)
  })

  it('returns false when different section mutated', () => {
    const mutated = {
      ...base,
      hooks: [{ id: '1', event: 'SessionStart', command: 'echo', enabled: true }],
    }
    expect(isSectionDirty(base, mutated, 'providers')).toBe(false)
  })

  it('returns false when either is null', () => {
    expect(isSectionDirty(null, base, 'providers')).toBe(false)
    expect(isSectionDirty(base, null, 'providers')).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// HOOK_EVENTS
// ---------------------------------------------------------------------------

describe('HOOK_EVENTS', () => {
  it('contains all five lifecycle events', () => {
    expect(HOOK_EVENTS).toContain('SessionStart')
    expect(HOOK_EVENTS).toContain('UserPromptSubmit')
    expect(HOOK_EVENTS).toContain('PreToolUse')
    expect(HOOK_EVENTS).toContain('PostToolUse')
    expect(HOOK_EVENTS).toContain('Stop')
    expect(HOOK_EVENTS).toHaveLength(5)
  })
})

// ---------------------------------------------------------------------------
// ProvidersTab smoke test
// ---------------------------------------------------------------------------

describe('ProvidersTab', () => {
  it('renders Anthropic / OpenAI / Codex inputs', async () => {
    const { ProvidersTab } = await import('./ProvidersTab')
    const draft = defaultSettings().providers
    render(
      <ProvidersTab
        draft={draft}
        isSaving={false}
        error={null}
        onUpdate={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    // Use getAllByRole to find the text inputs (password type) by their ids.
    expect(document.getElementById('anthropic-key')).toBeInTheDocument()
    expect(document.getElementById('openai-key')).toBeInTheDocument()
    expect(document.getElementById('codex-key')).toBeInTheDocument()
  })

  it('uses type=password for API key inputs (no logging)', async () => {
    const { ProvidersTab } = await import('./ProvidersTab')
    render(
      <ProvidersTab
        draft={defaultSettings().providers}
        isSaving={false}
        error={null}
        onUpdate={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    const anthropicInput = document.getElementById('anthropic-key')
    expect(anthropicInput).toHaveAttribute('type', 'password')
  })
})

// ---------------------------------------------------------------------------
// HooksTab smoke test
// ---------------------------------------------------------------------------

describe('HooksTab', () => {
  it('renders empty state message', async () => {
    const { HooksTab } = await import('./HooksTab')
    render(
      <HooksTab
        draft={[]}
        isSaving={false}
        error={null}
        onUpdate={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    expect(screen.getByText(/no hooks configured/i)).toBeInTheDocument()
  })

  it('calls onUpdate with new row on Add click', async () => {
    const { HooksTab } = await import('./HooksTab')
    const onUpdate = vi.fn()
    render(
      <HooksTab
        draft={[]}
        isSaving={false}
        error={null}
        onUpdate={onUpdate}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    fireEvent.click(screen.getByText(/add hook/i))
    expect(onUpdate).toHaveBeenCalledOnce()
    const arg = onUpdate.mock.calls[0][0] as unknown[]
    expect(Array.isArray(arg)).toBe(true)
    expect(arg).toHaveLength(1)
  })
})

// ---------------------------------------------------------------------------
// ContextPrivacyTab
// ---------------------------------------------------------------------------

describe('ContextPrivacyTab', () => {
  it('renders personalRoot, sitesRoot inputs and the chat-sync toggle', async () => {
    const { ContextPrivacyTab } = await import('./ContextPrivacyTab')
    const { context, memory } = defaultSettings()
    render(
      <ContextPrivacyTab
        context={context}
        memory={memory}
        isSaving={false}
        error={null}
        onUpdateContext={vi.fn()}
        onUpdateMemory={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    expect(document.getElementById('personal-root')).toBeInTheDocument()
    expect(document.getElementById('sites-root')).toBeInTheDocument()
    expect(document.getElementById('personal-chat-sync')).toBeInTheDocument()
  })

  it('personalChatSync toggle defaults to off (aria-checked=false)', async () => {
    const { ContextPrivacyTab } = await import('./ContextPrivacyTab')
    const { context, memory } = defaultSettings()
    render(
      <ContextPrivacyTab
        context={context}
        memory={memory}
        isSaving={false}
        error={null}
        onUpdateContext={vi.fn()}
        onUpdateMemory={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    const toggle = document.getElementById('personal-chat-sync')
    expect(toggle).toHaveAttribute('aria-checked', 'false')
  })

  it('clicking the chat-sync toggle calls onUpdateMemory with personalChatSync flipped', async () => {
    const { ContextPrivacyTab } = await import('./ContextPrivacyTab')
    const { context, memory } = defaultSettings()
    const onUpdateMemory = vi.fn()
    render(
      <ContextPrivacyTab
        context={context}
        memory={memory}
        isSaving={false}
        error={null}
        onUpdateContext={vi.fn()}
        onUpdateMemory={onUpdateMemory}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    fireEvent.click(document.getElementById('personal-chat-sync')!)
    expect(onUpdateMemory).toHaveBeenCalledOnce()
    expect(onUpdateMemory).toHaveBeenCalledWith({ personalChatSync: true })
  })

  it('typing in personalRoot calls onUpdateContext with the new path', async () => {
    const { ContextPrivacyTab } = await import('./ContextPrivacyTab')
    const { context, memory } = defaultSettings()
    const onUpdateContext = vi.fn()
    render(
      <ContextPrivacyTab
        context={context}
        memory={memory}
        isSaving={false}
        error={null}
        onUpdateContext={onUpdateContext}
        onUpdateMemory={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    const input = document.getElementById('personal-root') as HTMLInputElement
    fireEvent.change(input, { target: { value: '/Users/alice/Downloads' } })
    expect(onUpdateContext).toHaveBeenCalledWith({ personalRoot: '/Users/alice/Downloads' })
  })

  it('clicking Save fires onSave', async () => {
    const { ContextPrivacyTab } = await import('./ContextPrivacyTab')
    const { context, memory } = defaultSettings()
    const onSave = vi.fn()
    render(
      <ContextPrivacyTab
        context={context}
        memory={memory}
        isSaving={false}
        error={null}
        onUpdateContext={vi.fn()}
        onUpdateMemory={vi.fn()}
        onSave={onSave}
        onDiscard={vi.fn()}
      />
    )
    fireEvent.click(screen.getByText(/save context & privacy/i))
    expect(onSave).toHaveBeenCalledOnce()
  })
})

// ---------------------------------------------------------------------------
// McpServersTab smoke — env parsing edge cases
// ---------------------------------------------------------------------------

describe('McpServersTab smoke', () => {
  it('renders empty state message', async () => {
    const { McpServersTab } = await import('./McpServersTab')
    render(
      <McpServersTab
        draft={[]}
        isSaving={false}
        error={null}
        onUpdate={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    expect(screen.getByText(/no mcp servers configured/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// WidgetsTab
// ---------------------------------------------------------------------------

describe('WidgetsTab', () => {
  it('renders empty state message when no widgets configured', async () => {
    const { WidgetsTab } = await import('./WidgetsTab')
    render(
      <WidgetsTab
        draft={{ positions: {} }}
        isSaving={false}
        error={null}
        onUpdate={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    expect(screen.getByText(/no widgets configured/i)).toBeInTheDocument()
  })

  it('renders geometry editor rows for existing widgets', async () => {
    const { WidgetsTab } = await import('./WidgetsTab')
    render(
      <WidgetsTab
        draft={{
          positions: {
            'macos-widget': { x: 10, y: 20, width: 320, height: 200 },
          },
        }}
        isSaving={false}
        error={null}
        onUpdate={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    expect(screen.getByText('macos-widget')).toBeInTheDocument()
    expect(document.getElementById('widget-macos-widget-x')).toBeInTheDocument()
    expect(document.getElementById('widget-macos-widget-y')).toBeInTheDocument()
    expect(document.getElementById('widget-macos-widget-width')).toBeInTheDocument()
    expect(document.getElementById('widget-macos-widget-height')).toBeInTheDocument()
  })

  it('calls onUpdate with new widget when Add is clicked', async () => {
    const { WidgetsTab } = await import('./WidgetsTab')
    const onUpdate = vi.fn()
    render(
      <WidgetsTab
        draft={{ positions: {} }}
        isSaving={false}
        error={null}
        onUpdate={onUpdate}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    const slugInput = document.getElementById('new-widget-slug') as HTMLInputElement
    fireEvent.change(slugInput, { target: { value: 'linux-widget' } })
    fireEvent.click(screen.getByText(/add widget/i))
    expect(onUpdate).toHaveBeenCalledOnce()
    const arg = onUpdate.mock.calls[0][0]
    expect(arg.positions).toHaveProperty('linux-widget')
    expect(arg.positions['linux-widget']).toEqual({ x: 100, y: 100, width: 320, height: 200 })
  })

  it('calls onUpdate with updated geometry when a field changes', async () => {
    const { WidgetsTab } = await import('./WidgetsTab')
    const onUpdate = vi.fn()
    render(
      <WidgetsTab
        draft={{
          positions: {
            'macos-widget': { x: 10, y: 20, width: 320, height: 200 },
          },
        }}
        isSaving={false}
        error={null}
        onUpdate={onUpdate}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    const xInput = document.getElementById('widget-macos-widget-x') as HTMLInputElement
    fireEvent.change(xInput, { target: { value: '42' } })
    expect(onUpdate).toHaveBeenCalledOnce()
    const arg = onUpdate.mock.calls[0][0]
    expect(arg.positions['macos-widget'].x).toBe(42)
  })

  it('calls onSave when Save button clicked', async () => {
    const { WidgetsTab } = await import('./WidgetsTab')
    const onSave = vi.fn()
    render(
      <WidgetsTab
        draft={{ positions: {} }}
        isSaving={false}
        error={null}
        onUpdate={vi.fn()}
        onSave={onSave}
        onDiscard={vi.fn()}
      />
    )
    fireEvent.click(screen.getByText(/save widgets/i))
    expect(onSave).toHaveBeenCalledOnce()
  })
})

// ---------------------------------------------------------------------------
// HarnessBridgesTab
// ---------------------------------------------------------------------------

describe('HarnessBridgesTab', () => {
  it('renders three harness rows with Auto-Detect buttons enabled', async () => {
    const { HarnessBridgesTab } = await import('./HarnessBridgesTab')
    render(
      <HarnessBridgesTab
        draft={{}}
        isSaving={false}
        error={null}
        onUpdate={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    expect(screen.getByLabelText('Auto-detect Claude Code (CC) config path')).not.toBeDisabled()
    expect(screen.getByLabelText('Auto-detect OpenCode (OC) config path')).not.toBeDisabled()
    expect(screen.getByLabelText('Auto-detect Codex config path')).not.toBeDisabled()
  })

  it('shows "requires desktop app" when Auto-Detect clicked outside Tauri', async () => {
    const { HarnessBridgesTab } = await import('./HarnessBridgesTab')
    render(
      <HarnessBridgesTab
        draft={{}}
        isSaving={false}
        error={null}
        onUpdate={vi.fn()}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    fireEvent.click(screen.getByLabelText('Auto-detect Claude Code (CC) config path'))
    expect(await screen.findByText(/requires the desktop app/i)).toBeInTheDocument()
  })

  it('calls invoke and fills path when Auto-Detect finds a config in Tauri env', async () => {
    const { invoke } = await import('@tauri-apps/api/core')
    const { HarnessBridgesTab } = await import('./HarnessBridgesTab')

    // Simulate Tauri runtime
    ;(window as unknown as Record<string, unknown>).__TAURI__ = {}

    const mockInvoke = vi.mocked(invoke)
    mockInvoke.mockResolvedValueOnce('/home/user/.claude/settings.json')

    const onUpdate = vi.fn()
    render(
      <HarnessBridgesTab
        draft={{}}
        isSaving={false}
        error={null}
        onUpdate={onUpdate}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
      />
    )
    fireEvent.click(screen.getByLabelText('Auto-detect Claude Code (CC) config path'))
    expect(await screen.findByText(/Detected:.*settings\.json/)).toBeInTheDocument()
    expect(onUpdate).toHaveBeenCalledWith(
      expect.objectContaining({ ccConfigPath: '/home/user/.claude/settings.json' })
    )

    // Cleanup
    delete (window as unknown as Record<string, unknown>).__TAURI__
  })
})
