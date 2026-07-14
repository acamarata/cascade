/**
 * Purpose: Reusable tab bar component that renders a horizontal tab strip and
 *   optionally wraps child content below it. Driven by parent-controlled state
 *   (activeTab + onTabChange) so tab state can live in the URL.
 * Inputs:  tabs — array of { id, label, icon }; activeTab — current tab id;
 *          onTabChange — called with the new tab id on click; children — optional
 *          content rendered below the tab bar.
 * Outputs: <div> containing the tab strip and optional children slot.
 * Constraints: No internal state — purely controlled. Icon is a Lucide component type.
 * SPORT: MASTER-COMPONENTS.md — TabsLayout
 */

import { type ComponentType } from 'react'
import { cn } from '../../lib/utils'

export interface TabDef {
  id: string
  label: string
  icon?: ComponentType<{ className?: string }>
}

interface TabsLayoutProps {
  tabs: ReadonlyArray<TabDef>
  activeTab: string
  onTabChange: (id: string) => void
  children?: React.ReactNode
}

export function TabsLayout({ tabs, activeTab, onTabChange, children }: TabsLayoutProps) {
  return (
    <div className="flex flex-col h-full">
      {/* Tab strip */}
      <div className="flex border-b border-border shrink-0">
        {tabs.map((tab) => {
          const Icon = tab.icon
          const isActive = tab.id === activeTab
          return (
            <button
              key={tab.id}
              onClick={() => onTabChange(tab.id)}
              className={cn(
                'flex items-center gap-2 px-4 py-2 text-sm font-medium transition-colors',
                'border-b-2 -mb-px',
                isActive
                  ? 'border-primary text-primary'
                  : 'border-transparent text-muted-foreground hover:text-foreground hover:border-border'
              )}
            >
              {Icon && <Icon className="w-4 h-4" />}
              {tab.label}
            </button>
          )
        })}
      </div>

      {/* Optional content slot */}
      {children && <div className="flex-1 overflow-auto">{children}</div>}
    </div>
  )
}

export default TabsLayout
