import { useSearchParams } from 'react-router-dom'
import { FolderOpen, Brain, UserCircle2, BookText, GitBranch } from 'lucide-react'
import { TabsLayout } from '../../components/layout/TabsLayout'
import { VaultPage } from '../VaultPage'
import { MemoryPage } from '../MemoryPage'
import { PersonalVaultPage } from '../PersonalVaultPage'
import { InstructionsPage } from '../InstructionsPage'
import { VaultGraphPage } from '../VaultGraphPage'

const TABS = [
  { id: 'files',        label: 'Files',        icon: FolderOpen   },
  { id: 'memory',       label: 'Memory',       icon: Brain        },
  { id: 'personal',     label: 'Personal',     icon: UserCircle2  },
  { id: 'instructions', label: 'Instructions', icon: BookText     },
  { id: 'graph',        label: 'Graph',        icon: GitBranch    },
] as const

type TabId = (typeof TABS)[number]['id']

export default function VaultHub() {
  const [searchParams, setSearchParams] = useSearchParams()
  const raw = searchParams.get('tab')
  const activeTab: TabId = TABS.some(t => t.id === raw) ? (raw as TabId) : 'files'

  function handleTabChange(id: string) {
    setSearchParams(prev => {
      const next = new URLSearchParams(prev)
      next.set('tab', id)
      return next
    })
  }

  return (
    <div className="h-full flex flex-col">
      <TabsLayout tabs={TABS} activeTab={activeTab} onTabChange={handleTabChange} />
      <div className="flex-1 overflow-auto">
        {activeTab === 'files'        && <VaultPage />}
        {activeTab === 'memory'       && <MemoryPage />}
        {activeTab === 'personal'     && <PersonalVaultPage />}
        {activeTab === 'instructions' && <InstructionsPage />}
        {activeTab === 'graph'        && <VaultGraphPage />}
      </div>
    </div>
  )
}
