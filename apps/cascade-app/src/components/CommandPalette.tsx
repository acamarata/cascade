/**
 * Purpose: Global ⌘K command palette — renders all registered commands from the typed
 *   CommandRegistry, filters via cmdk built-in fuzzy search, and executes the selected
 *   action while closing the dialog.
 * Inputs:  isOpen (boolean), onClose (callback to close and restore focus).
 * Outputs: Radix Dialog + cmdk Command component with grouped, keyboard-navigable items.
 * Constraints: Focus trapped inside the dialog by Radix Dialog (aria-modal).
 *   All keyboard navigation (ArrowUp/Down/Enter/Escape) provided by cmdk.
 *   Commands are read from globalRegistry on every open so additions are always visible.
 *   Accepts both `isOpen` (ticket spec) and legacy `open` prop for compat; isOpen wins.
 * SPORT: MASTER-COMPONENTS.md — CommandPalette, src/components/CommandPalette.tsx
 */

import * as Dialog from '@radix-ui/react-dialog'
import { Command } from 'cmdk'
import { Search } from 'lucide-react'
import { useEffect, useState } from 'react'
import { globalRegistry, type CommandAction } from '../lib/commands/registry'

interface CommandPaletteProps {
  /** Primary prop per T-P3-E01-13 spec. */
  isOpen?: boolean
  /** Legacy compat — used until callers are updated to isOpen. */
  open?: boolean
  onClose: () => void
}

export function CommandPalette({ isOpen, open, onClose }: CommandPaletteProps) {
  const resolvedOpen = isOpen ?? open ?? false

  const [commands, setCommands] = useState<CommandAction[]>([])

  // Re-read the registry every time the palette opens so we always see fresh commands.
  useEffect(() => {
    if (resolvedOpen) {
      setCommands(globalRegistry.getAll())
    }
  }, [resolvedOpen])

  function handleSelect(command: CommandAction) {
    command.onExecute()
    onClose()
  }

  // Derive unique group names in insertion order.
  const groups = Array.from(new Set(commands.map((c) => c.group ?? 'Other')))

  return (
    <Dialog.Root open={resolvedOpen} onOpenChange={(v) => !v && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0" />
        <Dialog.Content
          className="fixed left-1/2 top-[20%] z-50 w-full max-w-xl -translate-x-1/2 rounded-xl border border-border bg-popover shadow-2xl outline-none data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95"
          aria-label="Command palette"
          aria-modal
          role="dialog"
        >
          <Command className="flex h-full w-full flex-col overflow-hidden rounded-xl">
            <div className="flex items-center border-b border-border px-3">
              <Search className="mr-2 h-4 w-4 shrink-0 text-muted-foreground" />
              <Command.Input
                className="flex h-11 w-full rounded-md bg-transparent py-3 text-sm outline-none placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50"
                placeholder="Type a command or search…"
                // eslint-disable-next-line jsx-a11y/no-autofocus
                autoFocus
              />
            </div>

            <Command.List className="max-h-[300px] overflow-y-auto overflow-x-hidden p-1">
              <Command.Empty className="py-6 text-center text-sm text-muted-foreground">
                No results found.
              </Command.Empty>

              {groups.map((group) => (
                <Command.Group
                  key={group}
                  heading={group}
                  className="overflow-hidden p-1 text-foreground [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted-foreground"
                >
                  {commands
                    .filter((cmd) => (cmd.group ?? 'Other') === group)
                    .map((cmd) => (
                      <Command.Item
                        key={cmd.id}
                        value={[cmd.label, ...(cmd.keywords ?? [])].join(' ')}
                        onSelect={() => handleSelect(cmd)}
                        className="relative flex cursor-pointer select-none items-center gap-2 rounded-md px-2 py-1.5 text-sm outline-none aria-selected:bg-accent aria-selected:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50"
                      >
                        <span className="flex-1">{cmd.label}</span>
                        {cmd.shortcut && (
                          <kbd className="ml-auto text-xs text-muted-foreground opacity-60">
                            {cmd.shortcut}
                          </kbd>
                        )}
                      </Command.Item>
                    ))}
                </Command.Group>
              ))}
            </Command.List>
          </Command>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
