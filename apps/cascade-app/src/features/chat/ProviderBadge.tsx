/**
 * Purpose: Small inline badge showing which provider backend served a response.
 *   Indicates "local fallback" with a distinct style when the provider id starts
 *   with "local:" (offline LLM). Shown per-message below the assistant bubble.
 * Inputs: servedBy string | undefined — provider id from the served_by SSE event.
 *   isLocalFallback boolean — true when provider id starts with "local:".
 * Outputs: Small pill badge, or null when servedBy is empty.
 * Constraints: Pure presentational, no hooks.
 * SPORT: E-P9-03 in-app chat — ProviderBadge
 */
import { Cpu, Cloud } from 'lucide-react'
import { cn } from '@/lib/utils'

interface ProviderBadgeProps {
  servedBy?: string | null
  isLocalFallback?: boolean
  className?: string
}

export function ProviderBadge({ servedBy, isLocalFallback, className }: ProviderBadgeProps) {
  if (!servedBy) return null

  // Strip "local:" prefix for display
  const displayName = isLocalFallback ? servedBy.replace(/^local:/, '') : servedBy

  return (
    <div
      className={cn(
        'inline-flex items-center gap-1 text-[0.6rem] rounded-full px-1.5 py-0.5 mt-1',
        isLocalFallback
          ? 'bg-amber-500/15 text-amber-600 dark:text-amber-400 border border-amber-500/30'
          : 'bg-muted text-muted-foreground border border-muted-foreground/20',
        className,
      )}
      title={isLocalFallback ? `Local LLM fallback: ${displayName}` : `Served by: ${displayName}`}
      aria-label={`Served by ${displayName}${isLocalFallback ? ' (local fallback)' : ''}`}
    >
      {isLocalFallback ? (
        <Cpu className="h-2.5 w-2.5 shrink-0" aria-hidden="true" />
      ) : (
        <Cloud className="h-2.5 w-2.5 shrink-0" aria-hidden="true" />
      )}
      <span>{displayName}</span>
      {isLocalFallback && (
        <span className="text-[0.55rem] opacity-70">local</span>
      )}
    </div>
  )
}
