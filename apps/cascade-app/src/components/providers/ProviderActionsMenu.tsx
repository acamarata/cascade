/**
 * Purpose: Dropdown action menu for a single provider row in ProviderSettings.
 *   Offers "Test Connection" and "Remove" actions.
 * Inputs:
 *   - providerId: string — stable provider id (e.g. "anthropic")
 *   - onTestConnection: () => void — called when user chooses Test Connection
 *   - onRemove: () => void — called when user confirms Remove
 *   - disabled: boolean — disables all menu items (used during pending operations)
 * Outputs: Radix DropdownMenu trigger button with two menu items
 * Constraints:
 *   - Remove action requires a confirm step (inline; not a separate dialog).
 *   - Test Connection fires immediately with no extra confirm.
 *   - WCAG 2.1 AA: focus rings, aria labels, keyboard navigable.
 * SPORT: MASTER-COMPONENTS.md — ProviderActionsMenu — components/providers/ — T-P3-E04-23
 */

import { useState } from 'react'
import { MoreHorizontal, Zap, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

interface ProviderActionsMenuProps {
  providerId: string
  onTestConnection: () => void
  onRemove: () => void
  disabled?: boolean
}

/**
 * Three-dot dropdown with Test Connection and Remove actions.
 *
 * Remove uses a two-click confirm flow inside the menu to prevent accidental
 * deletion without needing a separate dialog component.
 *
 * @param providerId - Stable provider id (for aria labelling)
 * @param onTestConnection - Invoked immediately on click
 * @param onRemove - Invoked after user confirms removal
 * @param disabled - Disables all items (use during async operations)
 */
export function ProviderActionsMenu({
  providerId,
  onTestConnection,
  onRemove,
  disabled = false,
}: ProviderActionsMenuProps) {
  const [confirmDelete, setConfirmDelete] = useState(false)

  function handleRemoveClick() {
    if (confirmDelete) {
      onRemove()
      setConfirmDelete(false)
    } else {
      setConfirmDelete(true)
    }
  }

  function handleOpenChange(open: boolean) {
    if (!open) setConfirmDelete(false)
  }

  return (
    <DropdownMenu onOpenChange={handleOpenChange}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          aria-label={`Actions for ${providerId} provider`}
          disabled={disabled}
        >
          <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        <DropdownMenuItem
          onClick={onTestConnection}
          disabled={disabled}
          className="gap-2 cursor-pointer"
        >
          <Zap className="h-4 w-4" aria-hidden="true" />
          Test Connection
        </DropdownMenuItem>

        <DropdownMenuSeparator />

        <DropdownMenuItem
          onClick={handleRemoveClick}
          disabled={disabled}
          className={
            confirmDelete
              ? 'gap-2 cursor-pointer text-destructive focus:text-destructive font-medium'
              : 'gap-2 cursor-pointer text-destructive focus:text-destructive'
          }
        >
          <Trash2 className="h-4 w-4" aria-hidden="true" />
          {confirmDelete ? 'Confirm Remove' : 'Remove'}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
