/**
 * Purpose: Track the active chat scope (personal | projects | cascade),
 *   optional selectedProjectId for the projects scope, and optional
 *   selectedTopicId for focusing the Personal scope on one topic
 *   (TopicSelector.tsx).
 * Inputs:  setScope action, setSelectedProject action, setSelectedTopic action.
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
  /** Focused topic id for the Personal scope, or null = view all topics. */
  selectedTopicId: string | null
  setScope: (scope: ChatScope) => void
  setSelectedProject: (projectId: string | null) => void
  setSelectedTopic: (topicId: string | null) => void
}

export const createChatScopeSlice: StateCreator<
  AppStore,
  [['zustand/immer', never]],
  [],
  ChatScopeSlice
> = (set) => ({
  chatScope: 'personal',
  selectedProjectId: null,
  selectedTopicId: null,
  setScope: (scope) =>
    set((draft) => {
      draft.chatScope = scope
    }),
  setSelectedProject: (projectId) =>
    set((draft) => {
      draft.selectedProjectId = projectId
    }),
  setSelectedTopic: (topicId) =>
    set((draft) => {
      draft.selectedTopicId = topicId
    }),
})
