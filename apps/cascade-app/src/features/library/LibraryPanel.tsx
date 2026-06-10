/**
 * Purpose: Library panel — the main /library route component.
 *   Four kind tabs (Prompts / Agents / Personas / Commands), live search,
 *   tag filter, item cards, detail view, delete with confirm dialog.
 * Inputs:  None (self-contained; fetches via useLibraryItems IPC hook).
 * Outputs: Full-height library panel.
 * Constraints:
 *   - onEdit / onCreate are seams for T-P3-E07-06 (author form) — wired as no-ops here.
 *   - Filter state is lifted here; LibrarySearch + TagFilter are pure controlled.
 *   - Tag list is derived from tab-scoped items (before search/tag filter).
 *   - Delete: confirm dialog via window.confirm (simple; no modal library needed).
 *   - Loading: Skeleton cards; empty: text illustration per kind.
 * SPORT: MASTER-COMPONENTS.md — LibraryPanel (T-P3-E07-04)
 *        MASTER-ROUTES.md — /library (T-P3-E07-04)
 */

import React, { useCallback, useMemo, useState } from 'react'
import { BookOpen, Plus } from 'lucide-react'
import { invoke } from '@tauri-apps/api/core'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '../../components/ui/tabs'
import { Skeleton } from '../../components/Skeleton'
import { useLibraryItems } from './useLibraryItems'
import { ItemCard } from './ItemCard'
import { LibrarySearch } from './LibrarySearch'
import { TagFilter } from './TagFilter'
import { applyFilters, uniqueTags } from './libraryFilters'
import { LibraryItemForm } from './editor/LibraryItemForm'
import type { LibraryItem, LibraryItemKind } from '../../types/library'

// ── Types ─────────────────────────────────────────────────────────────────────

type KindTab = LibraryItemKind

interface TabConfig {
  kind: KindTab
  label: string
  emptyText: string
}

// ── Tab configuration ─────────────────────────────────────────────────────────

const TABS: TabConfig[] = [
  { kind: 'prompt', label: 'Prompts', emptyText: 'No prompts yet. Create one to get started.' },
  { kind: 'agent', label: 'Agents', emptyText: 'No agents yet.' },
  { kind: 'persona', label: 'Personas', emptyText: 'No personas yet.' },
  { kind: 'slash-command', label: 'Commands', emptyText: 'No slash-commands yet.' },
]

// ── Skeleton loader ───────────────────────────────────────────────────────────

function SkeletonCards(): React.ReactElement {
  return (
    <div
      className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3"
      aria-busy="true"
      aria-label="Loading library items"
    >
      {Array.from({ length: 6 }).map((_, i) => (
        <div key={i} className="rounded-lg border border-border p-3">
          <Skeleton className="mb-2 h-4 w-3/4" />
          <Skeleton className="mb-1 h-3 w-full" />
          <Skeleton className="h-3 w-2/3" />
        </div>
      ))}
    </div>
  )
}

// ── Empty state ───────────────────────────────────────────────────────────────

function EmptyState({ message }: { message: string }): React.ReactElement {
  return (
    <div
      className="flex flex-col items-center justify-center gap-3 py-16 text-center"
      role="status"
      aria-label="No items found"
    >
      <BookOpen className="h-10 w-10 text-muted-foreground/40" aria-hidden="true" />
      <p className="text-sm text-muted-foreground">{message}</p>
    </div>
  )
}

// ── Detail view ───────────────────────────────────────────────────────────────

interface DetailViewProps {
  item: LibraryItem
  onClose: () => void
  onEdit?: (item: LibraryItem) => void
}

function DetailView({ item, onClose, onEdit }: DetailViewProps): React.ReactElement {
  return (
    <div
      className="absolute inset-0 z-10 flex flex-col overflow-auto bg-background p-6"
      role="dialog"
      aria-modal="true"
      aria-label={`Detail: ${item.title}`}
    >
      <div className="mb-4 flex items-center gap-2">
        <button
          type="button"
          onClick={onClose}
          aria-label="Back to library"
          className="rounded-md px-2 py-1 text-sm text-muted-foreground hover:bg-accent/30 hover:text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
        >
          ← Back
        </button>
        {onEdit && (
          <button
            type="button"
            onClick={() => onEdit(item)}
            aria-label={`Edit ${item.title}`}
            className="ml-auto rounded-md px-3 py-1 text-sm text-muted-foreground hover:bg-accent/30 hover:text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
          >
            Edit
          </button>
        )}
      </div>

      <h2 className="mb-1 text-xl font-semibold text-foreground">{item.title}</h2>
      <p className="mb-4 text-sm text-muted-foreground">{item.description}</p>

      {item.tags.length > 0 && (
        <div className="flex flex-wrap gap-1" role="list" aria-label="Tags">
          {item.tags.map((tag) => (
            <span
              key={tag}
              role="listitem"
              className="inline-flex items-center rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground"
            >
              {tag}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

// ── Tab panel ─────────────────────────────────────────────────────────────────

interface TabPanelProps {
  kind: KindTab
  emptyText: string
  searchQuery: string
  selectedTags: string[]
  onTagsChange: (tags: string[]) => void
  onToggleTag: (tag: string) => void
  onClearTags: () => void
  onItemDeleted: () => void
  onEdit?: (item: LibraryItem) => void
}

function TabPanel({
  kind,
  emptyText,
  searchQuery,
  selectedTags,
  onTagsChange: _onTagsChange,
  onToggleTag,
  onClearTags,
  onItemDeleted,
  onEdit,
}: TabPanelProps): React.ReactElement {
  const { items, loading, error, refetch } = useLibraryItems(kind)
  const [selectedItem, setSelectedItem] = useState<LibraryItem | null>(null)

  // Tags derived from all tab items (before search/tag filter — full palette)
  const available = useMemo(() => uniqueTags(items), [items])

  // Filtered items for display
  const filtered = useMemo(
    () => applyFilters(items, searchQuery, selectedTags),
    [items, searchQuery, selectedTags],
  )

  const handleDelete = useCallback(
    async (item: LibraryItem) => {
      if (!window.confirm(`Delete "${item.title}"? This cannot be undone.`)) return
      try {
        await invoke('delete_library_item', { id: item.id, kind: item.kind })
        if (selectedItem?.id === item.id) setSelectedItem(null)
        refetch()
        onItemDeleted()
      } catch (err) {
        console.error('Failed to delete library item:', err)
      }
    },
    [selectedItem, refetch, onItemDeleted],
  )

  if (selectedItem) {
    return (
      <div className="relative flex-1">
        <DetailView item={selectedItem} onClose={() => setSelectedItem(null)} onEdit={onEdit} />
      </div>
    )
  }

  if (loading) return <SkeletonCards />

  if (error) {
    return (
      <div role="alert" className="py-8 text-center text-sm text-destructive">
        Failed to load items: {error}
      </div>
    )
  }

  if (filtered.length === 0 && !searchQuery && selectedTags.length === 0) {
    return <EmptyState message={emptyText} />
  }

  if (filtered.length === 0) {
    return (
      <EmptyState message="No items match your search or filters." />
    )
  }

  return (
    <>
      {/* Tag filter — derived from tab items, shown above the grid */}
      <div className="mb-3 flex items-center gap-2">
        <TagFilter
          availableTags={available}
          selectedTags={selectedTags}
          onToggleTag={onToggleTag}
          onClearTags={onClearTags}
        />
        <span className="text-xs text-muted-foreground">
          {filtered.length} {filtered.length === 1 ? 'item' : 'items'}
        </span>
      </div>

      <div
        className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3"
        role="list"
        aria-label={`${kind} items`}
      >
        {filtered.map((item) => (
          <div key={item.id} role="listitem">
            <ItemCard
              item={item}
              onClick={() => setSelectedItem(item)}
              onEdit={onEdit}
              onDelete={handleDelete}
            />
          </div>
        ))}
      </div>
    </>
  )
}

// ── LibraryPanel ──────────────────────────────────────────────────────────────

/**
 * Root library panel routed at /library.
 *
 * Seams for T-P3-E07-06 author form:
 *   onEdit(item) — called when the edit button in ItemCard or DetailView is clicked.
 *   onCreate() — called from a "+ New" button (not yet rendered; seam wired as console.log).
 */
export function LibraryPanel(): React.ReactElement {
  const [activeKind, setActiveKind] = useState<KindTab>('prompt')
  const [searchQuery, setSearchQuery] = useState('')
  const [selectedTags, setSelectedTags] = useState<string[]>([])

  // Author form state (T-P3-E07-06)
  const [formOpen, setFormOpen] = useState(false)
  const [editingItem, setEditingItem] = useState<Partial<LibraryItem> | undefined>(undefined)

  const handleEdit = useCallback((item: LibraryItem) => {
    setEditingItem(item)
    setFormOpen(true)
  }, [])

  const handleCreate = useCallback(() => {
    setEditingItem({ kind: activeKind })
    setFormOpen(true)
  }, [activeKind])

  const handleFormSaved = useCallback(() => {
    // Panel auto-refreshes via the "library-items-changed" window event
    // dispatched by useUpsertLibraryItem on success.
  }, [])

  const handleToggleTag = useCallback((tag: string) => {
    setSelectedTags((prev) =>
      prev.includes(tag) ? prev.filter((t) => t !== tag) : [...prev, tag],
    )
  }, [])

  const handleClearTags = useCallback(() => {
    setSelectedTags([])
  }, [])

  const handleClearFilters = useCallback(() => {
    setSearchQuery('')
    setSelectedTags([])
  }, [])

  const hasActiveFilters = searchQuery !== '' || selectedTags.length > 0

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Page heading */}
      <header className="flex shrink-0 items-center gap-2 border-b border-border px-6 py-3">
        <BookOpen className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-lg font-semibold">Library</h1>
        <span className="text-sm text-muted-foreground">
          prompts · agents · personas · commands
        </span>

        {/* Search + actions — right-aligned */}
        <div className="ml-auto flex items-center gap-2">
          <LibrarySearch searchQuery={searchQuery} onSearchChange={setSearchQuery} />
          {hasActiveFilters && (
            <button
              type="button"
              onClick={handleClearFilters}
              aria-label="Clear all filters"
              className="rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-accent/30 hover:text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
            >
              Clear filters
            </button>
          )}
          <button
            type="button"
            onClick={handleCreate}
            aria-label="Create new library item"
            className="flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground hover:bg-accent/30 hover:text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
          >
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            New
          </button>
        </div>
      </header>

      {/* Tab strip + content */}
      <div className="flex flex-1 flex-col overflow-hidden px-6 pt-4">
        <Tabs
          value={activeKind}
          onValueChange={(v) => {
            setActiveKind(v as KindTab)
            // Reset tag filter when changing tabs (tags are tab-scoped)
            setSelectedTags([])
          }}
          className="flex flex-1 flex-col overflow-hidden"
        >
          <TabsList className="mb-4 shrink-0">
            {TABS.map((tab) => (
              <TabsTrigger key={tab.kind} value={tab.kind}>
                {tab.label}
              </TabsTrigger>
            ))}
          </TabsList>

          {TABS.map((tab) => (
            <TabsContent
              key={tab.kind}
              value={tab.kind}
              className="relative flex-1 overflow-auto"
            >
              <TabPanel
                kind={tab.kind}
                emptyText={tab.emptyText}
                searchQuery={searchQuery}
                selectedTags={selectedTags}
                onTagsChange={setSelectedTags}
                onToggleTag={handleToggleTag}
                onClearTags={handleClearTags}
                onItemDeleted={() => {}}
                onEdit={handleEdit}
              />
            </TabsContent>
          ))}
        </Tabs>
      </div>

      {/* Author form dialog — wires onEdit/onCreate seams (T-P3-E07-06) */}
      <LibraryItemForm
        open={formOpen}
        onOpenChange={setFormOpen}
        initialItem={editingItem}
        onSaved={handleFormSaved}
      />
    </div>
  )
}
