# MASTER-APPS.md — Cascade Application Surfaces

**Purpose:** Registry of every app/surface in the Cascade project.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-06-09
**Source:** Cascade P2/P3/P4 plan

## Hooks (cascade-dashboard)

| Hook | Path | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| useChat | apps/cascade-dashboard/src/hooks/useChat.ts | SSE client for /api/chat streaming | ✅ Done | P3 | T-P3-E02-21 |
| useChatHistory | apps/cascade-dashboard/src/hooks/useChatHistory.ts | localStorage-backed chat session history | ✅ Done | P3 | T-P3-E02-20 |

## Features

| Feature | Description | Status | Phase | Wave |
|---|---|---|---|---|
| GP Chat | Floating chat panel with SSE streaming, 10-tool catalog, markdown + syntax highlighting, tool-invocation cards | ✅ Done | P3 | W-05 |

## Apps

| App | Path | Stack | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|---|
| cascade-app | apps/cascade-app/ | Tauri 2 + React + Vite + Tailwind | Browser/desktop dashboard | 🔲 Planned | P3 | T-P3-E01-* |
| cascade-widget-macos | src/widget/macos/ | Swift + WidgetKit + xcodegen | macOS WidgetKit quota/status widget; Xcode project at CascadeWidget.xcodeproj; targets CascadeApp + CascadeWidgetExtension; App Group group.io.cascade; project spec at src/widget/macos/project.yml | 🟡 Partial | P2 | T-P2-E04-* |
| cascade-tray-macos | src/widget/macos/CascadeApp/ | Swift + NSStatusItem + SwiftUI | macOS menubar status icon (NSStatusItem) + popover (MenubarPopoverView); MenubarController manages icon state and 30s refresh timer; AppDelegate in CascadeAppApp.swift wires lifecycle | 🟡 Partial | P2 | T-P2-E04-09 (MenubarController+MenubarPopoverView done); T-P2-E04-10 (IPC-aware updateIcon pending) |
| cascade-widget-linux | apps/cascade-widget-linux/ | JS (GNOME Shell Extension) + Python (KDE Plasmoid) | Linux desktop widgets | 🔲 Planned | P2 | T-P2-E03-* |
| cascade-widget-windows | apps/cascade-widget-windows/ | WinUI 3 / Windows App SDK | Windows widget + system tray | 🔲 Planned | P2 | T-P2-E05-* |
