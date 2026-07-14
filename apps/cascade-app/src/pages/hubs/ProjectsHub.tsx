/**
 * Purpose: Hub page aggregating PEWS, Project Map, Context, and RAG Explorer
 *   under a single tabbed layout at /projects (or wherever routed).
 *   ?tab= query param drives active tab so browser back/forward works.
 * Inputs:  ?tab=pews|map|context|rag (default: 'pews')
 * Outputs: Full-height tabbed page; selected tab content fills remaining space.
 * Constraints:
 *   - Tab state lives exclusively in URL (?tab=); no local useState for active tab.
 *   - Unknown ?tab values fall back to 'pews'.
 * SPORT: MASTER-COMPONENTS.md — ProjectsHub
 */

import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Network, GitFork, ClipboardList, Database } from 'lucide-react'
import { TabsLayout } from '../../components/layout/TabsLayout'
import { PewsPage } from '../PewsPage'
import { ProjectMapPage } from '../ProjectMapPage'
import { ContextPanel } from '../../features/context/ContextPanel'
import { RagExplorerPage } from '../RagExplorerPage'

// ── Types ────────────────────────────────────────────────────────────────────

type TabId = 'pews' | 'map' | 'context' | 'rag'

const VALID_TABS: ReadonlySet<string> = new Set<TabId>(['pews', 'map', 'context', 'rag'])

function isTabId(value: string | null): value is TabId {
  return value !== null && VALID_TABS.has(value)
}

// ── Tab definitions ───────────────────────────────────────────────────────────

const TABS = [
  { id: 'pews' as TabId, label: 'PEWS', icon: Network },
  { id: 'map' as TabId, label: 'Project Map', icon: GitFork },
  { id: 'context' as TabId, label: 'Context', icon: ClipboardList },
  { id: 'rag' as TabId, label: 'RAG Explorer', icon: Database },
]

// ── Component ─────────────────────────────────────────────────────────────────

export default function ProjectsHub() {
  const [searchParams, setSearchParams] = useSearchParams()

  const rawTab = searchParams.get('tab')
  const activeTab: TabId = isTabId(rawTab) ? rawTab : 'pews'

  const handleTabChange = useCallback(
    (id: string) => {
      setSearchParams((prev) => {
        const next = new URLSearchParams(prev)
        next.set('tab', id)
        return next
      })
    },
    [setSearchParams]
  )

  return (
    <div className="h-full flex flex-col">
      <TabsLayout tabs={TABS} activeTab={activeTab} onTabChange={handleTabChange} />
      <div className="flex-1 overflow-auto">
        {activeTab === 'pews' && <PewsPage />}
        {activeTab === 'map' && <ProjectMapPage />}
        {activeTab === 'context' && <ContextPanel />}
        {activeTab === 'rag' && <RagExplorerPage />}
      </div>
    </div>
  )
}
