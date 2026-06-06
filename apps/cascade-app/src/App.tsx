/**
 * Purpose: Root application shell — BrowserRouter, route tree, global keyboard shortcuts.
 * Inputs:  None (ThemeProvider in main.tsx handles theme; RouterApp owns the route tree).
 * Outputs: BrowserRouter wrapping RouterApp + CommandPalette portal.
 * Constraints: BrowserRouter works with Tauri 2 custom protocol (tauri://localhost).
 *   Theme is applied by ThemeProvider (main.tsx) — no manual useEffect here.
 *   CommandPalette is a portal rendered above all routes; ⌘K / Ctrl+K opens it.
 * SPORT: MASTER-COMPONENTS.md — App
 */

import { useEffect } from "react";
import { BrowserRouter } from "react-router-dom";
import { CommandPalette } from "./components/CommandPalette";
import { RouterApp } from "./routes/index";
import { useCommandPalette } from "./hooks/useCommandPalette";

export default function App() {
  const { isOpen, open, close } = useCommandPalette();

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
    <BrowserRouter>
      {/* Command palette renders as a portal above all routes */}
      <CommandPalette open={isOpen} onClose={close} />
      <RouterApp />
    </BrowserRouter>
  );
}
