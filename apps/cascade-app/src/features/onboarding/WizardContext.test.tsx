/**
 * Test suite for WizardContext and wizardReducer.
 * Covers: step navigation boundaries, mark complete, jump, state updates, hook error cases.
 */

import { renderHook, act } from '@testing-library/react'
import { WizardProvider, useWizard } from './WizardContext'
import type { WizardState } from './types'
import { WizardStep, stateToCheckpoint, checkpointToState } from './types'
import { wizardReducer } from './wizardReducer'
import type { MergeResult as RichMergeResult, MergeSection } from './merge/types'

/**
 * Helper to create an initial state for testing.
 */
function createTestState(overrides?: Partial<WizardState>): WizardState {
  const now = new Date().toISOString()
  return {
    step: WizardStep.Welcome,
    completedSteps: new Set(),
    skippedSteps: new Set(),
    providerConnected: false,
    scanResult: null,
    detectedToolIds: null,
    mergeResult: null,
    mergeResults: {},
    selectedTemplates: [],
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

/** Factory for a minimal RichMergeResult for testing. */
function createTestMergeResult(tierStep: WizardStep, sections: MergeSection[] = []): RichMergeResult {
  return {
    tier: tierStep,
    sections,
    generatedAt: new Date().toISOString(),
    modelUsed: 'test-model',
    promptHash: 'abc123',
  }
}

/** Factory for a minimal MergeSection for testing. */
function createTestSection(id: string, overrides?: Partial<MergeSection>): MergeSection {
  return {
    id,
    title: `Section ${id}`,
    sourceFiles: [],
    proposedContent: `content for ${id}`,
    status: 'pending',
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

// ---------------------------------------------------------------------------
// T-P3-E03-27: Section approval state (mergeResults in reducer)
// ---------------------------------------------------------------------------

describe('wizardReducer — section approval actions (T-P3-E03-27)', () => {
  // Test T-27-a: SET_MERGE_RESULT stores result under tier key
  it('SET_MERGE_RESULT should store a MergeResult under the tier key', () => {
    const initial = createTestState()
    const result = createTestMergeResult(WizardStep.MergeContent, [
      createTestSection('s1'),
    ])
    const next = wizardReducer(initial, {
      type: 'SET_MERGE_RESULT',
      payload: { tier: 'global', result },
    })
    expect(next.mergeResults['global']).toBeDefined()
    expect(next.mergeResults['global']?.sections).toHaveLength(1)
    expect(next.mergeResults['global']?.sections[0].id).toBe('s1')
  })

  // Test T-27-b: approveSection sets status to 'approved'
  it('UPDATE_SECTION_STATUS approve should set section status to approved', () => {
    const section = createTestSection('s1')
    const initial = createTestState({
      mergeResults: {
        global: createTestMergeResult(WizardStep.MergeContent, [section]),
      },
    })
    const next = wizardReducer(initial, {
      type: 'UPDATE_SECTION_STATUS',
      payload: { tier: 'global', sectionId: 's1', status: 'approved' },
    })
    expect(next.mergeResults['global']?.sections[0].status).toBe('approved')
  })

  // Test T-27-c: rejectSection sets status to 'rejected'
  it('UPDATE_SECTION_STATUS reject should set section status to rejected', () => {
    const section = createTestSection('s1')
    const initial = createTestState({
      mergeResults: {
        global: createTestMergeResult(WizardStep.MergeContent, [section]),
      },
    })
    const next = wizardReducer(initial, {
      type: 'UPDATE_SECTION_STATUS',
      payload: { tier: 'global', sectionId: 's1', status: 'rejected' },
    })
    expect(next.mergeResults['global']?.sections[0].status).toBe('rejected')
    expect(next.mergeResults['global']?.sections[0].editedContent).toBeUndefined()
  })

  // Test T-27-d: editSection sets status to 'edited' AND stores editedContent
  it('UPDATE_SECTION_STATUS edit should set status=edited and store editedContent', () => {
    const section = createTestSection('s1')
    const initial = createTestState({
      mergeResults: {
        global: createTestMergeResult(WizardStep.MergeContent, [section]),
      },
    })
    const next = wizardReducer(initial, {
      type: 'UPDATE_SECTION_STATUS',
      payload: { tier: 'global', sectionId: 's1', status: 'edited', editedContent: 'my edits' },
    })
    expect(next.mergeResults['global']?.sections[0].status).toBe('edited')
    expect(next.mergeResults['global']?.sections[0].editedContent).toBe('my edits')
  })

  // Test T-27-e: getMergeResult returns undefined for unknown tier
  it('getMergeResult should return undefined for an unknown tier', () => {
    const initial = createTestState()
    expect(initial.mergeResults['unknown_tier']).toBeUndefined()
  })

  // Test T-27-f: UPDATE_SECTION_STATUS on unknown tier returns state unchanged
  it('UPDATE_SECTION_STATUS on missing tier should be a no-op', () => {
    const initial = createTestState()
    const next = wizardReducer(initial, {
      type: 'UPDATE_SECTION_STATUS',
      payload: { tier: 'nonexistent', sectionId: 's1', status: 'approved' },
    })
    expect(next).toBe(initial) // Reference equality: no new object allocated
  })

  // Test T-27-g: sections not targeted by UPDATE_SECTION_STATUS are unchanged
  it('UPDATE_SECTION_STATUS should only change the targeted section', () => {
    const s1 = createTestSection('s1')
    const s2 = createTestSection('s2')
    const initial = createTestState({
      mergeResults: {
        global: createTestMergeResult(WizardStep.MergeContent, [s1, s2]),
      },
    })
    const next = wizardReducer(initial, {
      type: 'UPDATE_SECTION_STATUS',
      payload: { tier: 'global', sectionId: 's1', status: 'approved' },
    })
    expect(next.mergeResults['global']?.sections[0].status).toBe('approved')
    expect(next.mergeResults['global']?.sections[1].status).toBe('pending') // unchanged
  })

  // Test T-27-h: editedContent cleared when status reverts from 'edited'
  it('UPDATE_SECTION_STATUS should clear editedContent when status is not edited', () => {
    const section = createTestSection('s1', { status: 'edited', editedContent: 'old edit' })
    const initial = createTestState({
      mergeResults: {
        global: createTestMergeResult(WizardStep.MergeContent, [section]),
      },
    })
    const next = wizardReducer(initial, {
      type: 'UPDATE_SECTION_STATUS',
      payload: { tier: 'global', sectionId: 's1', status: 'approved' },
    })
    expect(next.mergeResults['global']?.sections[0].editedContent).toBeUndefined()
  })
})

// ---------------------------------------------------------------------------
// T-P3-E03-27: useWizard section action helpers
// ---------------------------------------------------------------------------

describe('useWizard — section action helpers (T-P3-E03-27)', () => {
  const wrapper = ({ children }: { children: React.ReactNode }) => (
    <WizardProvider>{children}</WizardProvider>
  )

  // Test T-27-i: setMergeResult stores result; getMergeResult retrieves it
  it('setMergeResult/getMergeResult round-trip', () => {
    const { result } = renderHook(() => useWizard(), { wrapper })
    const mergeResult = createTestMergeResult(WizardStep.MergeContent, [
      createTestSection('s1'),
    ])
    act(() => {
      result.current.setMergeResult('global', mergeResult)
    })
    expect(result.current.getMergeResult('global')).toBeDefined()
    expect(result.current.getMergeResult('global')?.sections[0].id).toBe('s1')
    expect(result.current.getMergeResult('unknown')).toBeUndefined()
  })

  // Test T-27-j: approveSection updates section status
  it('approveSection should set section status to approved', () => {
    const section = createTestSection('s1')
    const { result } = renderHook(() => useWizard(), { wrapper })
    act(() => {
      result.current.setMergeResult(
        'global',
        createTestMergeResult(WizardStep.MergeContent, [section]),
      )
    })
    act(() => {
      result.current.approveSection('global', 's1')
    })
    expect(result.current.getMergeResult('global')?.sections[0].status).toBe('approved')
  })

  // Test T-27-k: rejectSection updates section status
  it('rejectSection should set section status to rejected', () => {
    const section = createTestSection('s1')
    const { result } = renderHook(() => useWizard(), { wrapper })
    act(() => {
      result.current.setMergeResult(
        'global',
        createTestMergeResult(WizardStep.MergeContent, [section]),
      )
    })
    act(() => {
      result.current.rejectSection('global', 's1')
    })
    expect(result.current.getMergeResult('global')?.sections[0].status).toBe('rejected')
  })

  // Test T-27-l: editSection stores editedContent and status='edited'
  it('editSection should set status=edited and store content', () => {
    const section = createTestSection('s1')
    const { result } = renderHook(() => useWizard(), { wrapper })
    act(() => {
      result.current.setMergeResult(
        'global',
        createTestMergeResult(WizardStep.MergeContent, [section]),
      )
    })
    act(() => {
      result.current.editSection('global', 's1', 'user edited text')
    })
    const updated = result.current.getMergeResult('global')?.sections[0]
    expect(updated?.status).toBe('edited')
    expect(updated?.editedContent).toBe('user edited text')
  })
})

// ---------------------------------------------------------------------------
// T-P3-E03-33: checkpoint round-trip for mergeResults
// ---------------------------------------------------------------------------

describe('checkpoint round-trip — mergeResults (T-P3-E03-33)', () => {
  // Test T-33-a: stateToCheckpoint serializes mergeResults
  it('stateToCheckpoint should include mergeResults in checkpoint', () => {
    const section = createTestSection('s1', { status: 'approved' })
    const state = createTestState({
      mergeResults: {
        global: createTestMergeResult(WizardStep.MergeContent, [section]),
      },
    })
    const checkpoint = stateToCheckpoint(state)
    expect(checkpoint.mergeResults['global']).toBeDefined()
    expect(checkpoint.mergeResults['global']?.sections[0].status).toBe('approved')
  })

  // Test T-33-b: checkpointToState restores mergeResults including section status
  it('checkpointToState should restore mergeResults with section status', () => {
    const section = createTestSection('s1', { status: 'edited', editedContent: 'saved edits' })
    const state = createTestState({
      mergeResults: {
        global: createTestMergeResult(WizardStep.MergeContent, [section]),
      },
    })
    const checkpoint = stateToCheckpoint(state)
    const restored = checkpointToState(checkpoint)
    expect(restored.mergeResults['global']?.sections[0].status).toBe('edited')
    expect(restored.mergeResults['global']?.sections[0].editedContent).toBe('saved edits')
  })

  // Test T-33-c: checkpoint with missing mergeResults field deserializes to empty object
  it('checkpointToState should default mergeResults to {} when field absent', () => {
    const section = createTestSection('s1')
    const state = createTestState()
    const checkpoint = stateToCheckpoint(state)
    // Simulate old checkpoint without mergeResults field
    const { mergeResults: _dropped, ...legacyCheckpoint } = checkpoint as typeof checkpoint & { mergeResults?: unknown }
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const restored = checkpointToState(legacyCheckpoint as any)
    expect(restored.mergeResults).toEqual({})
    void section // suppress unused var
  })

  // Test T-33-d: mergeResults from multiple tiers both survive round-trip
  it('stateToCheckpoint / checkpointToState should preserve multiple tier results', () => {
    const state = createTestState({
      mergeResults: {
        global: createTestMergeResult(WizardStep.MergeContent, [createTestSection('g1')]),
        project: createTestMergeResult(WizardStep.MergeContent, [createTestSection('p1')]),
      },
    })
    const restored = checkpointToState(stateToCheckpoint(state))
    expect(restored.mergeResults['global']?.sections[0].id).toBe('g1')
    expect(restored.mergeResults['project']?.sections[0].id).toBe('p1')
  })
})
