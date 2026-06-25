/**
 * Purpose: Hub page for Settings, Templates, and Library — three tabs in one surface.
 * Inputs:  ?tab= search param (general | templates | library). Defaults to 'general'.
 * Outputs: Full-height tabbed layout rendering the active tab's feature panel.
 * Constraints:
 *   - Tab state persisted in URL via useSearchParams (deep-linkable, back-button safe).
 *   - Each tab renders its own scroll context; this page is flex layout only.
 * SPORT: MASTER-ROUTES.md — /settings/hub
 */

import { useSearchParams } from 'react-router-dom'
import { Settings2, LayoutGrid, BookOpen } from 'lucide-react'
import { TabsLayout } from '../../components/layout/TabsLayout'
import { SettingsPage } from '../SettingsPage'
import { TemplateBrowser } from '../TemplateBrowser'
import { LibraryPanel } from '../../features/library/LibraryPanel'

type TabId = 'general' | 'templates' | 'library'

const TABS = [
  { id: 'general' as TabId, label: 'General', icon: Settings2 },
  { id: 'templates' as TabId, label: 'Templates', icon: LayoutGrid },
  { id: 'library' as TabId, label: 'Library', icon: BookOpen },
]

export function SettingsHub() {
  const [searchParams, setSearchParams] = useSearchParams()
  const raw = searchParams.get('tab')
  const activeTab: TabId =
    raw === 'templates' || raw === 'library' ? raw : 'general'

  function handleTabChange(id: string) {
    setSearchParams({ tab: id }, { replace: true })
  }

  return (
    <div className="h-full flex flex-col">
      <TabsLayout tabs={TABS} activeTab={activeTab} onTabChange={handleTabChange}>
        {activeTab === 'general' && <SettingsPage />}
        {activeTab === 'templates' && <TemplateBrowser />}
        {activeTab === 'library' && <LibraryPanel />}
      </TabsLayout>
    </div>
  )
}
