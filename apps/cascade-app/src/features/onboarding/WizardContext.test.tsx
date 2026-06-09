/**
 * Test suite for WizardContext and wizardReducer.
 * Covers: step navigation boundaries, mark complete, jump, state updates, hook error cases.
 */

import { renderHook, act } from '@testing-library/react'
import { WizardProvider, useWizard } from './WizardContext'
import type { WizardState } from './types'
import { WizardStep } from './types'
import { wizardReducer } from './wizardReducer'

/**
 * Helper to create an initial state for testing.
 */
function createTestState(overrides?: Partial<WizardState>): WizardState {
  const now = new Date().toISOString()
  return {
    step: WizardStep.Welcome,
    completedSteps: new Set(),
    providerConnected: false,
    scanResult: null,
    detectedToolIds: null,
    mergeResult: null,
    toolModes: {},
    archiveManifestPath: null,
    archivedTools: {},
    symlinkPlanApplied: false,
    daemonInstalled: false,
    startedAt: now,
    updatedAt: now,
    ...overrides,
  }
}

describe('wizardReducer', () => {
  // Test 1: goBack() at step 1 (Welcome) leaves step unchanged
  it('should not go below step 1 (Welcome) on BACK', () => {
    const initial = createTestState()
    const result = wizardReducer(initial, { type: 'BACK' })
    expect(result.step).toBe(WizardStep.Welcome)
    expect(result.step).toBe(1)
  })

  // Test 2: goNext() at step 10 (Done) leaves step unchanged
  it('should not go beyond step 10 (Done) on NEXT', () => {
    const atDone = createTestState({ step: WizardStep.Done })
    const result = wizardReducer(atDone, { type: 'NEXT' })
    expect(result.step).toBe(WizardStep.Done)
    expect(result.step).toBe(10)
  })

  // Test 3: jumpTo() sets currentStep to target regardless of current
  it('should jump to any valid step via JUMP_TO', () => {
    const initial = createTestState()
    const result = wizardReducer(initial, {
      type: 'JUMP_TO',
      payload: WizardStep.MergeContent,
    })
    expect(result.step).toBe(WizardStep.MergeContent)
    expect(result.step).toBe(4)
  })

  // Test 4: NEXT advances step by 1
  it('should advance one step on NEXT', () => {
    const initial = createTestState()
    const result = wizardReducer(initial, { type: 'NEXT' })
    expect(result.step).toBe(WizardStep.ProviderConnect)
    expect(result.step).toBe(2)
  })

  // Test 5: MARK_COMPLETE adds step to completedSteps and advances
  it('should add step to completedSteps and advance on MARK_COMPLETE', () => {
    const initial = createTestState()
    const result = wizardReducer(initial, {
      type: 'MARK_COMPLETE',
      payload: WizardStep.Welcome,
    })
    expect(result.completedSteps.has(WizardStep.Welcome)).toBe(true)
    expect(result.step).toBe(WizardStep.ProviderConnect)
  })

  // Test 6: UPDATE_STATE merges partial updates
  it('should merge partial state on UPDATE_STATE', () => {
    const initial = createTestState()
    const result = wizardReducer(initial, {
      type: 'UPDATE_STATE',
      payload: {
        providerConnected: true,
        daemonInstalled: true,
      },
    })
    expect(result.providerConnected).toBe(true)
    expect(result.daemonInstalled).toBe(true)
    expect(result.step).toBe(initial.step)
  })

  // Test T-21-a: UPDATE_ARCHIVE_STATUS sets archivedTools[toolId] = true
  it('UPDATE_ARCHIVE_STATUS should mark a tool as archived', () => {
    const initial = createTestState()
    const result = wizardReducer(initial, {
      type: 'UPDATE_ARCHIVE_STATUS',
      payload: { toolId: 'claude-code', archived: true },
    })
    expect(result.archivedTools['claude-code']).toBe(true)
    // Other tools untouched
    expect(result.archivedTools['opencode']).toBeUndefined()
    // Step unchanged
    expect(result.step).toBe(initial.step)
  })

  // Test T-21-b: UPDATE_ARCHIVE_STATUS can set archived = false
  it('UPDATE_ARCHIVE_STATUS should be able to mark a tool as NOT archived', () => {
    const initial = createTestState({ archivedTools: { 'claude-code': true } })
    const result = wizardReducer(initial, {
      type: 'UPDATE_ARCHIVE_STATUS',
      payload: { toolId: 'claude-code', archived: false },
    })
    expect(result.archivedTools['claude-code']).toBe(false)
  })

  // Test T-21-c: UPDATE_ARCHIVE_STATUS merges multiple tools independently
  it('UPDATE_ARCHIVE_STATUS should preserve existing archivedTools entries', () => {
    const initial = createTestState({ archivedTools: { 'opencode': true } })
    const result = wizardReducer(initial, {
      type: 'UPDATE_ARCHIVE_STATUS',
      payload: { toolId: 'cursor', archived: true },
    })
    expect(result.archivedTools['opencode']).toBe(true)
    expect(result.archivedTools['cursor']).toBe(true)
  })

  // Test T-21-d: initial state has empty archivedTools
  it('initial state should have empty archivedTools', () => {
    const initial = createTestState()
    expect(initial.archivedTools).toEqual({})
  })
})

describe('useWizard hook', () => {
  const wrapper = ({ children }: { children: React.ReactNode }) => (
    <WizardProvider>{children}</WizardProvider>
  )

  // Test 7: useWizard returns context when inside WizardProvider
  it('should provide context when inside WizardProvider', () => {
    const { result } = renderHook(() => useWizard(), { wrapper })
    expect(result.current.currentStep).toBe(WizardStep.Welcome)
    expect(result.current.completedSteps).toBeInstanceOf(Set)
    expect(result.current.completedSteps.size).toBe(0)
  })

  // Test 8: useWizard throws when called outside WizardProvider
  it('should throw when called outside WizardProvider', () => {
    expect(() => {
      renderHook(() => useWizard())
    }).toThrow('useWizard must be called within a <WizardProvider>')
  })

  // Test 9: goNext() navigates forward
  it('goNext() should advance the current step', () => {
    const { result } = renderHook(() => useWizard(), { wrapper })
    expect(result.current.currentStep).toBe(WizardStep.Welcome)
    act(() => {
      result.current.goNext()
    })
    expect(result.current.currentStep).toBe(WizardStep.ProviderConnect)
  })

  // Test 10: goBack() navigates backward (within bounds)
  it('goBack() should go back one step', () => {
    const { result } = renderHook(() => useWizard(), { wrapper })
    // First advance to step 2
    act(() => {
      result.current.goNext()
    })
    expect(result.current.currentStep).toBe(WizardStep.ProviderConnect)
    // Then go back
    act(() => {
      result.current.goBack()
    })
    expect(result.current.currentStep).toBe(WizardStep.Welcome)
  })

  // Test 11: jumpTo() jumps to arbitrary step
  it('jumpTo() should jump to any valid step', () => {
    const { result } = renderHook(() => useWizard(), { wrapper })
    act(() => {
      result.current.jumpTo(WizardStep.VerifyDiff)
    })
    expect(result.current.currentStep).toBe(WizardStep.VerifyDiff)
    expect(result.current.currentStep).toBe(6)
  })

  // Test 12: markComplete() adds to completedSteps and advances
  it('markComplete() should track completed steps', () => {
    const { result } = renderHook(() => useWizard(), { wrapper })
    act(() => {
      result.current.markComplete(WizardStep.Welcome)
    })
    expect(result.current.completedSteps.has(WizardStep.Welcome)).toBe(true)
    expect(result.current.currentStep).toBe(WizardStep.ProviderConnect)
  })
})
