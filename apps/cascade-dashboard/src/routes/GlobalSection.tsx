/**
 * Purpose: Global section route — 8 GCI sub-panels fetching live daemon /api/gci/* endpoints.
 * Inputs: none (route-level component, no props).
 * Outputs: 2-column responsive grid with tabbed sub-navigation and all 8 panels.
 * Constraints: Each panel manages its own fetch + 30s refresh via useGCI.
 *   No shadcn/ui (not installed) — pure Tailwind classes throughout.
 * SPORT: T-P3-E02-09 GlobalSection
 */
import { useState } from 'react'
import { RulesPanel } from '@/components/global/RulesPanel'
import { ReferencesPanel } from '@/components/global/ReferencesPanel'
import { MemoryPanel } from '@/components/global/MemoryPanel'
import { AgentsPanel } from '@/components/global/AgentsPanel'
import { SkillsPanel } from '@/components/global/SkillsPanel'
import { SettingsSnapshotPanel } from '@/components/global/SettingsSnapshotPanel'
import { CascadeDiagramPanel } from '@/components/global/CascadeDiagramPanel'
import { HooksInspectorPanel } from '@/components/global/HooksInspectorPanel'
import { HooksList } from '@/components/HooksEditor/HooksList'
import { HarnessPanel } from '@/components/HarnessPanel/HarnessPanel'

type Tab = 'files' | 'config'

export function GlobalSection() {
  const [activeTab, setActiveTab] = useState<Tab>('files')

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h1 className="text-xl font-semibold text-foreground mb-1">Global</h1>
        <p className="text-muted-foreground text-sm">
          GCI — global Claude instructions, rules, references, memory, agents, and hooks.
        </p>
      </div>

      {/* Tab bar */}
      <div className="flex gap-1 border-b border-border">
        <button
          onClick={() => setActiveTab('files')}
          className={`px-4 py-2 text-sm font-medium rounded-t transition-colors ${
            activeTab === 'files'
              ? 'border-b-2 border-primary text-primary -mb-px'
              : 'text-muted-foreground hover:text-foreground'
          }`}
        >
          Files
        </button>
        <button
          onClick={() => setActiveTab('config')}
          className={`px-4 py-2 text-sm font-medium rounded-t transition-colors ${
            activeTab === 'config'
              ? 'border-b-2 border-primary text-primary -mb-px'
              : 'text-muted-foreground hover:text-foreground'
          }`}
        >
          Config
        </button>
      </div>

      {/* Files tab: 5 file-list panels (rules, references, memory, agents, skills) */}
      {activeTab === 'files' && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <RulesPanel />
          <ReferencesPanel />
          <MemoryPanel />
          <AgentsPanel />
          <SkillsPanel />
        </div>
      )}

      {/* Config tab: settings snapshot, cascade diagram, hooks inspector */}
      {activeTab === 'config' && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <SettingsSnapshotPanel />
          <CascadeDiagramPanel />
          <div className="md:col-span-2">
            <HooksInspectorPanel />
          </div>
          {/* T-P3-E02-27: Hooks editor — add/edit/delete hooks without hand-editing settings.json */}
          <div className="md:col-span-2">
            <section id="hooks-editor">
              <HooksList />
            </section>
          </div>
          {/* T-P3-E02-28: Harness augmentation panel — CC/OC integration status + regenerate */}
          <div className="md:col-span-2">
            <HarnessPanel />
          </div>
        </div>
      )}
    </div>
  )
}
