/**
 * Purpose: Dropdown selector for choosing the provider to route the next message
 *   through. "Auto" lets the daemon routing table decide (E-P7-07 priority chain).
 *   Populated from the providers list via the Tauri IPC command cascade_providers_list.
 *   When the active chat namespace is protected (personal/private) and the user
 *   pins an untrusted provider, shows an inline warning next to the selector —
 *   the deliberate-choice contract still lets the pin through, but the UI must
 *   surface that choice rather than stay silent about it.
 * Inputs: selectedProvider string | null (null = auto); onSelect callback;
 *   optional namespace string (drives the protected-namespace warning).
 * Outputs: Select dropdown with auto + registered provider ids, plus an inline
 *   warning when (protected namespace + untrusted provider pinned).
 * Constraints: Falls back to empty options (only "Auto") when IPC fails.
 *   Pure UI — does not mutate provider registry.
 * SPORT: E-P9-03 in-app chat — ProviderSelector
 */
import { useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { TriangleAlert } from 'lucide-react'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'
import { isProtectedNamespace, isProviderTrustedForSensitive } from './useChat'

interface ProviderItem {
  id: string
  name: string
  status: string
}

interface ProviderSelectorProps {
  /** null = auto (daemon routing) */
  selectedProvider: string | null
  onSelect: (provider: string | null) => void
  /** Active chat namespace — drives the untrusted-provider-in-protected-chat warning. */
  namespace?: string
}

export function ProviderSelector({ selectedProvider, onSelect, namespace }: ProviderSelectorProps) {
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

  // Deliberate-choice contract: the daemon still honors an explicit pin even
  // in a protected namespace (personal/private). This is the one place the
  // UI must say so — a warning, not a block.
  const showUntrustedPinWarning =
    isProtectedNamespace(namespace) &&
    Boolean(selectedProvider) &&
    !isProviderTrustedForSensitive(selectedProvider)

  return (
    <div className="flex items-center gap-1.5">
      <Select value={selectedProvider ?? 'auto'} onValueChange={handleChange}>
        <SelectTrigger className="h-7 text-xs w-32 shrink-0" aria-label="Select provider">
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
      {showUntrustedPinWarning && (
        <span
          role="alert"
          title="This provider is not trusted for private chat — this conversation will be sent to it because you pinned it. Unpin to stay on trusted providers."
          className={cn(
            'inline-flex items-center gap-1 text-[0.6rem] rounded-full px-1.5 py-0.5',
            'bg-amber-500/15 text-amber-600 dark:text-amber-400 border border-amber-500/30'
          )}
        >
          <TriangleAlert className="h-2.5 w-2.5 shrink-0" aria-hidden="true" />
          Untrusted pin
        </span>
      )}
    </div>
  )
}
