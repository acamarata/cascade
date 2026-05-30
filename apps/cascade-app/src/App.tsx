/**
 * Purpose: Root application shell — router, layout, global keyboard shortcuts.
 * Inputs:  None (reads config via TauriContext on mount)
 * Outputs: Rendered <AppLayout> with routes wired to feature panels
 * Constraints: react-router-dom v6 memory router (Tauri has no real URL bar).
 * SPORT: MASTER-COMPONENTS.md — App, AppLayout
 */

import { useEffect } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { CommandPalette } from "./components/CommandPalette";
import { AppLayout } from "./components/AppLayout";
import { CascadeEditor } from "./components/CascadeEditor";
import { InboxViewer } from "./components/InboxViewer";
import { RagExplorer } from "./components/RagExplorer";
import { SettingsPanel } from "./components/SettingsPanel";
import { StatusPage } from "./components/StatusPage";
import { useCommandPalette } from "./hooks/useCommandPalette";
import { useTheme } from "./hooks/useTheme";

export default function App() {
  const { isOpen, open, close } = useCommandPalette();
  const { theme } = useTheme();

  // Apply theme class to <html> element so Tailwind dark: variants work.
  // Reads from ~/ .cascade/config.json via Tauri command on first render.
  useEffect(() => {
    const root = document.documentElement;
    root.classList.remove("light", "dark");
    if (theme === "system") {
      const prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
      root.classList.add(prefersDark ? "dark" : "light");
    } else {
      root.classList.add(theme);
    }
  }, [theme]);

  // Global keyboard shortcut: ⌘K / Ctrl+K opens command palette.
  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        open();
      }
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open]);

  return (
    <MemoryRouter initialEntries={["/"]} initialIndex={0}>
      {/* Command palette renders as a portal above all routes */}
      <CommandPalette open={isOpen} onClose={close} />

      <Routes>
        <Route element={<AppLayout onOpenPalette={open} />}>
          <Route path="/" element={<CascadeEditor />} />
          <Route path="/status" element={<StatusPage />} />
          <Route path="/inbox" element={<InboxViewer />} />
          <Route path="/rag" element={<RagExplorer />} />
          <Route path="/settings" element={<SettingsPanel />} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}
