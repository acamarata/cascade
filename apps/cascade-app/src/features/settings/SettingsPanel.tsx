/**
 * Purpose: Full tabbed settings surface for the /settings route.
 *   Two-pane layout: left sidebar of tab links + right content area (VS Code style).
 *   Tabs: Context & Privacy, Providers, Gemini Pool, Harness Bridges, Hooks,
 *         Scheduled Tasks, Plugins, Widgets, Vault, MCP Servers.
 *   Each tab reads from useSettings (get_settings IPC) and saves via update_settings
 *   (per-section partial update pattern).
 * Inputs:  None — fetches settings on mount via useSettings hook.
 * Outputs: Full-height two-pane layout; tabs navigate via Radix Tabs (keyboard-accessible).
 * Constraints:
 *   - This replaces src/components/SettingsPanel.tsx as the /settings surface.
 *     The old SettingsPanel (AppConfig-based) is kept for the legacy panels it hosts
 *     (RestoreToolSection, ToolModeSection, ProviderSettingsSection).
 *   - Sidebar nav items are keyboard-navigable (Radix Tabs handles arrow-key navigation).
 *   - Default active tab: "providers".
 *   - WCAG 2.1 AA: landmark sections, focus ring, screen-reader live region for save toasts.
 * SPORT: MASTER-COMPONENTS.md — SettingsPanel (features) — T-P3-E07-15
 */

import { useState } from 'react'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { useSettings } from './useSettings'
import { ContextPrivacyTab } from './ContextPrivacyTab'
import { ProvidersTab } from './ProvidersTab'
import { GeminiPoolTab } from './GeminiPoolTab'
import { HarnessBridgesTab } from './HarnessBridgesTab'
import { HooksTab } from './HooksTab'
import { ScheduledTasksTab } from './ScheduledTasksTab'
import { PluginsTab } from './PluginsTab'
import { WidgetsTab } from './WidgetsTab'
import { VaultTab } from './VaultTab'
import { McpServersTab } from './McpServersTab'
import { FleetTab } from './FleetTab'
import type { CascadeSettings } from '@/types/settings'

// ── Tab definition ────────────────────────────────────────────────────────────

const TABS = [
  { id: 'context-privacy', label: 'Context & Privacy' },
  { id: 'providers', label: 'Providers' },
  { id: 'gemini-pool', label: 'Gemini Pool' },
  { id: 'harness-bridges', label: 'Harness Bridges' },
  { id: 'hooks', label: 'Hooks' },
  { id: 'scheduled-tasks', label: 'Scheduled Tasks' },
  { id: 'plugins', label: 'Plugins' },
  { id: 'widgets', label: 'Widgets' },
  { id: 'vault', label: 'Vault' },
  { id: 'mcp-servers', label: 'MCP Servers' },
  { id: 'fleet', label: 'Fleet' },
] as const

type TabId = (typeof TABS)[number]['id']

// ── Save status toast (minimal, no extra dep) ─────────────────────────────────

interface SaveToastProps {
  message: string | null
}

function SaveToast({ message }: SaveToastProps) {
  if (!message) return null
  return (
    <div
      role="status"
      aria-live="polite"
      aria-atomic="true"
      className="fixed bottom-4 right-4 z-50 rounded-md border border-border bg-background px-4 py-2 text-sm shadow-lg"
    >
      {message}
    </div>
  )
}

// ── SettingsPanel ─────────────────────────────────────────────────────────────

export interface SettingsPanelProps {
  /** Tab to open on mount. Defaults to 'providers'. Unknown values fall back silently. */
  initialTab?: TabId
}

export function SettingsPanel({ initialTab }: SettingsPanelProps = {}) {
  const { draft, isLoading, updateSection, saveSection, discard, error } = useSettings()
  const [activeTab, setActiveTab] = useState<TabId>(initialTab ?? 'providers')
  const [isSaving, setIsSaving] = useState(false)
  const [toast, setToast] = useState<string | null>(null)

  function showToast(msg: string) {
    setToast(msg)
    setTimeout(() => setToast(null), 2500)
  }

  async function handleSave(section: keyof CascadeSettings) {
    setIsSaving(true)
    await saveSection(section)
    setIsSaving(false)
    showToast('Settings saved.')
  }

  /** Context & Privacy spans two independent top-level sections; save both. */
  async function handleSaveContextPrivacy() {
    setIsSaving(true)
    await saveSection('context')
    await saveSection('memory')
    setIsSaving(false)
    showToast('Settings saved.')
  }

  function handleDiscard() {
    discard()
    showToast('Changes discarded.')
  }

  if (isLoading || !draft) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
        Loading settings…
      </div>
    )
  }

  return (
    <>
      <div className="flex h-full min-h-0">
        <Tabs
          value={activeTab}
          onValueChange={(v) => setActiveTab(v as TabId)}
          orientation="vertical"
          className="flex h-full w-full min-h-0"
        >
          {/* ── Left sidebar nav ──────────────────────────────────────────── */}
          <aside className="w-52 shrink-0 border-r border-border bg-muted/30">
            <nav aria-label="Settings navigation">
              <h1 className="px-4 pt-5 pb-3 text-sm font-semibold text-foreground">Settings</h1>
              <TabsList className="flex flex-col items-stretch h-auto bg-transparent p-0 gap-0">
                {TABS.map((tab) => (
                  <TabsTrigger
                    key={tab.id}
                    value={tab.id}
                    className={[
                      'justify-start px-4 py-2 text-sm rounded-none text-left',
                      'data-[state=active]:bg-background data-[state=active]:shadow-none',
                      'data-[state=active]:border-l-2 data-[state=active]:border-primary data-[state=active]:pl-[14px]',
                      'data-[state=inactive]:text-muted-foreground data-[state=inactive]:hover:text-foreground data-[state=inactive]:hover:bg-accent/50',
                    ].join(' ')}
                  >
                    {tab.label}
                  </TabsTrigger>
                ))}
              </TabsList>
            </nav>
          </aside>

          {/* ── Main content area ─────────────────────────────────────────── */}
          <main className="flex-1 overflow-y-auto p-6 min-w-0">
            {/* Error banner */}
            {error && (
              <div
                role="alert"
                className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-4 py-2 text-sm text-destructive"
              >
                {error}
              </div>
            )}

            <TabsContent value="context-privacy" className="mt-0 focus-visible:outline-none">
              <ContextPrivacyTab
                context={draft.context}
                memory={draft.memory}
                isSaving={isSaving}
                error={null}
                onUpdateContext={(v) => updateSection('context', v)}
                onUpdateMemory={(v) => updateSection('memory', v)}
                onSave={handleSaveContextPrivacy}
                onDiscard={handleDiscard}
              />
            </TabsContent>

            <TabsContent value="providers" className="mt-0 focus-visible:outline-none">
              <ProvidersTab
                draft={draft.providers}
                isSaving={isSaving}
                error={null}
                onUpdate={(v) => updateSection('providers', v)}
                onSave={() => handleSave('providers')}
                onDiscard={handleDiscard}
              />
            </TabsContent>

            <TabsContent value="gemini-pool" className="mt-0 focus-visible:outline-none">
              <GeminiPoolTab
                draft={draft.geminiPool}
                isSaving={isSaving}
                error={null}
                onUpdate={(v) => updateSection('geminiPool', v)}
                onSave={() => handleSave('geminiPool')}
                onDiscard={handleDiscard}
              />
            </TabsContent>

            <TabsContent value="harness-bridges" className="mt-0 focus-visible:outline-none">
              <HarnessBridgesTab
                draft={draft.harnessBridges}
                isSaving={isSaving}
                error={null}
                onUpdate={(v) => updateSection('harnessBridges', v)}
                onSave={() => handleSave('harnessBridges')}
                onDiscard={handleDiscard}
              />
            </TabsContent>

            <TabsContent value="hooks" className="mt-0 focus-visible:outline-none">
              <HooksTab
                draft={draft.hooks}
                isSaving={isSaving}
                error={null}
                onUpdate={(v) => updateSection('hooks', v)}
                onSave={() => handleSave('hooks')}
                onDiscard={handleDiscard}
              />
            </TabsContent>

            <TabsContent value="scheduled-tasks" className="mt-0 focus-visible:outline-none">
              <ScheduledTasksTab
                draft={draft.scheduledTasks}
                isSaving={isSaving}
                error={null}
                onUpdate={(v) => updateSection('scheduledTasks', v)}
                onSave={() => handleSave('scheduledTasks')}
                onDiscard={handleDiscard}
              />
            </TabsContent>

            <TabsContent value="plugins" className="mt-0 focus-visible:outline-none">
              <PluginsTab
                draft={draft.plugins}
                isSaving={isSaving}
                error={null}
                onUpdate={(v) => updateSection('plugins', v)}
                onSave={() => handleSave('plugins')}
                onDiscard={handleDiscard}
              />
            </TabsContent>

            <TabsContent value="widgets" className="mt-0 focus-visible:outline-none">
              <WidgetsTab draft={draft.widgets} />
            </TabsContent>

            <TabsContent value="vault" className="mt-0 focus-visible:outline-none">
              <VaultTab />
            </TabsContent>

            <TabsContent value="mcp-servers" className="mt-0 focus-visible:outline-none">
              <McpServersTab
                draft={draft.mcpServers}
                isSaving={isSaving}
                error={null}
                onUpdate={(v) => updateSection('mcpServers', v)}
                onSave={() => handleSave('mcpServers')}
                onDiscard={handleDiscard}
              />
            </TabsContent>

            <TabsContent value="fleet" className="mt-0 focus-visible:outline-none">
              <FleetTab />
            </TabsContent>
          </main>
        </Tabs>
      </div>

      <SaveToast message={toast} />
    </>
  )
}
