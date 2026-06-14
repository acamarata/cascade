/**
 * Purpose: Dropdown selector for choosing the provider to route the next message
 *   through. "Auto" lets the daemon routing table decide (E-P7-07 priority chain).
 *   Populated from the providers list via the Tauri IPC command cascade_providers_list.
 * Inputs: selectedProvider string | null (null = auto); onSelect callback.
 * Outputs: Select dropdown with auto + registered provider ids.
 * Constraints: Falls back to empty options (only "Auto") when IPC fails.
 *   Pure UI — does not mutate provider registry.
 * SPORT: E-P9-03 in-app chat — ProviderSelector
 */
import { useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

interface ProviderItem {
  id: string
  name: string
  status: string
}

interface ProviderSelectorProps {
  /** null = auto (daemon routing) */
  selectedProvider: string | null
  onSelect: (provider: string | null) => void
}

export function ProviderSelector({ selectedProvider, onSelect }: ProviderSelectorProps) {
  const [providers, setProviders] = useState<ProviderItem[]>([])

  useEffect(() => {
    invoke<ProviderItem[]>('cascade_providers_list')
      .then((list) => setProviders(list ?? []))
      .catch(() => {
        // IPC unavailable in test / dev — silently skip
        setProviders([])
      })
  }, [])

  function handleChange(value: string) {
    onSelect(value === 'auto' ? null : value)
  }

  return (
    <Select value={selectedProvider ?? 'auto'} onValueChange={handleChange}>
      <SelectTrigger
        className="h-7 text-xs w-32 shrink-0"
        aria-label="Select provider"
      >
        <SelectValue placeholder="Auto" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="auto" className="text-xs">
          Auto (routed)
        </SelectItem>
        {providers.map((p) => (
          <SelectItem key={p.id} value={p.id} className="text-xs">
            {p.name || p.id}
            {p.status === 'unhealthy' && (
              <span className="ml-1 text-[0.6rem] text-destructive">✕</span>
            )}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
