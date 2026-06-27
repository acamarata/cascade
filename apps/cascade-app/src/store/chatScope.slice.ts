/**
 * Purpose: Track the active chat scope (personal | projects | cascade) and
 *   optional selectedProjectId for the projects scope.
 * Inputs:  setScope action, setSelectedProject action.
 * Outputs: ChatScopeSlice — merged into AppStore via store/index.ts.
 * Constraints: Scope is kept in memory only (no localStorage persistence in P1).
 * SPORT: MASTER-HOOKS.md — ChatScopeSlice
 */
import type { StateCreator } from 'zustand'
import type { AppStore } from './index'

export type ChatScope = 'personal' | 'projects' | 'cascade'

export interface ChatScopeSlice {
  chatScope: ChatScope
  selectedProjectId: string | null
  setScope: (scope: ChatScope) => void
  setSelectedProject: (projectId: string | null) => void
}

export const createChatScopeSlice: StateCreator<
  AppStore,
  [['zustand/immer', never]],
  [],
  ChatScopeSlice
> = (set) => ({
  chatScope: 'personal',
  selectedProjectId: null,
  setScope: (scope) =>
    set((draft) => {
      draft.chatScope = scope
    }),
  setSelectedProject: (projectId) =>
    set((draft) => {
      draft.selectedProjectId = projectId
    }),
})
