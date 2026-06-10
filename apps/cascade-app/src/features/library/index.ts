/**
 * Purpose: Public barrel export for the cascade library feature.
 *   Exports the form component and hook for use by LibraryPanel and LibraryPage.
 * SPORT: MASTER-COMPONENTS.md — library feature barrel — features/library/ — T-P3-E07-06
 */

export { LibraryItemForm } from './editor/LibraryItemForm'
export type { LibraryItemFormProps } from './editor/LibraryItemForm'
export { useUpsertLibraryItem } from './useUpsertLibraryItem'
export type { UseUpsertLibraryItemOptions, UseUpsertLibraryItemResult } from './useUpsertLibraryItem'
export type { LibraryItem, LibraryItemKind, HarnessTarget, UpsertLibraryItemArgs, UpsertLibraryItemAck } from './types'
