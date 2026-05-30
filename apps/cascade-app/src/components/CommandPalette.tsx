/**
 * Purpose: ⌘K command palette — quick navigation and daemon actions.
 * Inputs:  open (boolean), onClose (callback)
 * Outputs: Radix Dialog containing a cmdk Command component
 * Constraints: Focus must be trapped inside the dialog (Radix Dialog handles this).
 *   All keyboard navigation (ArrowUp/Down/Enter/Escape) is provided by cmdk.
 *   The palette registers no global shortcut — App.tsx owns that.
 * SPORT: MASTER-COMPONENTS.md — CommandPalette
 */

import { useNavigate } from "react-router-dom";
import * as Dialog from "@radix-ui/react-dialog";
import { Command } from "cmdk";
import {
  FileText,
  Inbox,
  Search,
  Settings,
  Activity,
  RotateCcw,
  Power,
} from "lucide-react";

interface CommandPaletteProps {
  open: boolean;
  onClose: () => void;
}

interface CommandItem {
  id: string;
  label: string;
  group: string;
  icon: React.ReactNode;
  action: () => void;
  keywords?: string[];
}

export function CommandPalette({ open, onClose }: CommandPaletteProps) {
  const navigate = useNavigate();

  function nav(path: string) {
    navigate(path);
    onClose();
  }

  const items: CommandItem[] = [
    {
      id: "nav-editor",
      label: "Open CASCADE.md Editor",
      group: "Navigation",
      icon: <FileText className="h-4 w-4" />,
      action: () => nav("/"),
      keywords: ["cascade", "editor", "markdown"],
    },
    {
      id: "nav-status",
      label: "View Daemon Status",
      group: "Navigation",
      icon: <Activity className="h-4 w-4" />,
      action: () => nav("/status"),
      keywords: ["daemon", "health", "status"],
    },
    {
      id: "nav-inbox",
      label: "Open Inbox",
      group: "Navigation",
      icon: <Inbox className="h-4 w-4" />,
      action: () => nav("/inbox"),
      keywords: ["inbox", "messages", "pci"],
    },
    {
      id: "nav-rag",
      label: "RAG Explorer",
      group: "Navigation",
      icon: <Search className="h-4 w-4" />,
      action: () => nav("/rag"),
      keywords: ["rag", "search", "retrieval", "knowledge"],
    },
    {
      id: "nav-settings",
      label: "Settings",
      group: "Navigation",
      icon: <Settings className="h-4 w-4" />,
      action: () => nav("/settings"),
      keywords: ["config", "preferences", "theme"],
    },
    {
      id: "daemon-restart",
      label: "Restart Daemon",
      group: "Daemon",
      icon: <RotateCcw className="h-4 w-4" />,
      action: () => {
        // Tauri command invoked asynchronously; palette closes immediately.
        import("@tauri-apps/api/core").then(({ invoke }) => {
          invoke("stop_daemon")
            .then(() => invoke("start_daemon"))
            .catch(console.error);
        });
        onClose();
      },
      keywords: ["restart", "daemon", "cascaded"],
    },
    {
      id: "daemon-stop",
      label: "Stop Daemon",
      group: "Daemon",
      icon: <Power className="h-4 w-4" />,
      action: () => {
        import("@tauri-apps/api/core").then(({ invoke }) =>
          invoke("stop_daemon").catch(console.error)
        );
        onClose();
      },
      keywords: ["stop", "daemon", "cascaded", "kill"],
    },
  ];

  const groups = Array.from(new Set(items.map((i) => i.group)));

  return (
    <Dialog.Root open={open} onOpenChange={(v) => !v && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0" />
        <Dialog.Content
          className="fixed left-1/2 top-[20%] z-50 w-full max-w-xl -translate-x-1/2 rounded-xl border border-border bg-popover shadow-2xl outline-none data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95"
          aria-label="Command palette"
        >
          <Command className="flex h-full w-full flex-col overflow-hidden rounded-xl">
            <div className="flex items-center border-b border-border px-3">
              <Search className="mr-2 h-4 w-4 shrink-0 text-muted-foreground" />
              <Command.Input
                className="flex h-11 w-full rounded-md bg-transparent py-3 text-sm outline-none placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50"
                placeholder="Type a command or search..."
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
                  {items
                    .filter((item) => item.group === group)
                    .map((item) => (
                      <Command.Item
                        key={item.id}
                        value={`${item.label} ${item.keywords?.join(" ") ?? ""}`}
                        onSelect={item.action}
                        className="relative flex cursor-pointer select-none items-center gap-2 rounded-md px-2 py-1.5 text-sm outline-none aria-selected:bg-accent aria-selected:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50"
                      >
                        {item.icon}
                        <span>{item.label}</span>
                      </Command.Item>
                    ))}
                </Command.Group>
              ))}
            </Command.List>
          </Command>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
