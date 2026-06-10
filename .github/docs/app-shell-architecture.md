# Cascade App Shell Architecture

This document describes the structure of `cascade-app`, the Tauri 2 desktop GUI for cascade. It covers the directory layout, routing, layout chrome, theme system, command palette, multi-window setup, accessibility, and the IPC bridge to the `cascaded` daemon.

---

## Directory Structure

The app lives at `apps/cascade-app/` in the cascade workspace. The frontend code is under `src/` and the Rust backend under `src-tauri/`.

```
apps/cascade-app/
├── src/
│   ├── App.tsx                   # Root component — mounts Router and global providers
│   ├── routes/
│   │   └── index.tsx             # Centralised route table (RouterApp)
│   ├── layouts/
│   │   └── AppLayout.tsx         # Persistent chrome for authenticated routes
│   ├── pages/
│   │   ├── DashboardPage.tsx
│   │   ├── InboxPage.tsx
│   │   ├── OnboardingPage.tsx
│   │   ├── SearchPage.tsx
│   │   ├── SettingsPage.tsx
│   │   └── NotFoundPage.tsx
│   ├── components/
│   │   ├── layout/
│   │   │   ├── Sidebar.tsx       # Left-side navigation
│   │   │   ├── TopBar.tsx        # Header bar
│   │   │   └── StatusBar.tsx     # Footer status strip
│   │   ├── CommandPalette.tsx    # Cmd+K modal
│   │   ├── RouteAnnouncer.tsx    # ARIA live region for navigation
│   │   └── SkipToMain.tsx        # Skip-to-content link
│   ├── hooks/
│   │   ├── useCommandPalette.ts  # Open/close state for CommandPalette
│   │   └── useThemeStore.ts      # Re-export from store
│   ├── lib/
│   │   ├── commands/
│   │   │   └── registry.ts       # Command registry for the palette
│   │   └── ipc/
│   │       └── client.ts         # Typed IPC wrappers (the `ipc` singleton)
│   ├── store/
│   │   └── index.ts              # Zustand store — daemon, theme, window slices
│   └── types/
│       ├── errors.ts             # CascadeIpcError
│       └── ipc.ts                # Return-type interfaces for all 9 IPC commands
├── src-tauri/
│   ├── src/
│   │   ├── main.rs               # Tauri builder, window setup, command registration
│   │   ├── commands.rs           # All 9 #[tauri::command] handlers
│   │   └── ipc_client.rs         # Rust-side daemon socket client (JSON-RPC)
│   └── tauri.conf.json           # Window geometry, app identifier, capabilities
├── index.html
├── vite.config.ts
└── package.json
```

---

## Routing

`RouterApp` (in `src/routes/index.tsx`) is the route table. It is mounted inside a `BrowserRouter` in `App.tsx`. There are 7 route entries total:

| Path | Component | Chrome |
|---|---|---|
| `/` | Redirects to `/dashboard` | none |
| `/onboarding` | `OnboardingPage` | none (standalone wizard) |
| `/dashboard` | `DashboardPage` | AppLayout |
| `/inbox` | `InboxPage` | AppLayout |
| `/search` | `SearchPage` | AppLayout |
| `/settings` | `SettingsPage` | AppLayout |
| `*` | `NotFoundPage` | AppLayout |

The four app routes (`/dashboard`, `/inbox`, `/search`, `/settings`) and the catch-all are nested under the `<AppLayout>` element route. The `/onboarding` route sits outside that nest so it renders without sidebar or topbar.

The root redirect uses `replace={true}` to avoid stacking a history entry.

---

## Layout Chrome

`AppLayout` (`src/layouts/AppLayout.tsx`) is the persistent shell for all authenticated routes. It composes four elements in a full-height flex layout:

```
┌─────────────────────────────────────────┐
│  SkipToMain (visually-hidden until focus) │
│  RouteAnnouncer (ARIA live region)        │
├─────────┬───────────────────────────────┤
│         │  TopBar                        │
│ Sidebar │─────────────────────────────── │
│         │  <Outlet /> (page content)     │
│         │─────────────────────────────── │
│         │  StatusBar                     │
└─────────┴───────────────────────────────┘
│  CommandPalette (portal, conditionally)  │
└─────────────────────────────────────────┘
```

`Sidebar` renders the left navigation column. `TopBar` sits at the top of the right column. Page content renders via `<Outlet />` in a scrollable `<main>` element with `id="main-content"`. `StatusBar` sits at the bottom of the right column showing daemon health and context info.

`CommandPalette` is portal-rendered over the full window. It mounts once per `AppLayout` mount and opens/closes via the `useCommandPalette` hook.

---

## Theme System

The theme system is driven by a Zustand slice (`useThemeStore`) that persists the active theme to `localStorage`. On mount, the store reads the persisted value and applies a `data-theme` attribute to the document root.

The command palette exposes theme-switching commands via `registerDefaultCommands`, which receives `setTheme` from the store. Users can switch themes without navigating to Settings.

Tailwind CSS uses `data-theme` selectors for theming. The base design tokens (background, foreground, etc.) are CSS custom properties defined per-theme.

---

## Command Palette

The command palette opens on Cmd+K (macOS) or Ctrl+K (Windows/Linux). It is implemented in `CommandPalette.tsx` and uses the `useCommandPalette` hook for open/close state.

Commands are registered via `registerDefaultCommands(navigate, setTheme)`, which runs in a `useEffect` inside `AppLayout` once per mount. Registration is idempotent: re-registering a command by id replaces the existing entry. This means HMR remounts and React StrictMode double-invokes have no side effects.

The command registry (`src/lib/commands/registry.ts`) is a plain module-level map, not a context or store. Commands are plain objects with an id, label, and action callback.

---

## Multi-Window

Tauri 2 supports multiple windows. The `main` window is created in `main.rs` at startup using `tauri.conf.json` geometry. Additional windows (such as a compact widget or detached panel) are created programmatically via the Tauri window API when needed.

Each window shares the same Rust command set. The Zustand store is scoped to the WebView instance, so each window maintains independent UI state. Daemon state (daemon health, config) is fetched on demand via IPC rather than pushed from the daemon.

---

## IPC Bridge

The frontend communicates with the `cascaded` daemon entirely through 9 Tauri commands defined in `src-tauri/src/commands.rs`. The Rust handlers serialize requests to JSON-RPC 2.0, send them over a Unix domain socket, and deserialize the daemon response.

The TypeScript `ipc` singleton (`src/lib/ipc/client.ts`) wraps all 9 commands. It is the only place in the frontend that calls `invoke`. Components and hooks call `ipc.getStatus()`, `ipc.search(query)`, etc., never `invoke` directly.

Errors propagate as `CascadeIpcError` instances with a `code` string and `message`. The `code` matches the Rust error variant name (e.g. `DaemonNotRunning`, `ResourceNotFound`). See [tauri-ipc.md](tauri-ipc.md) for the full command reference.

The IPC contract is frozen at schema v1. Commands are append-only: existing command names, parameter names, and return-type shapes are never changed or removed.

---

## Accessibility

`AppLayout` includes two a11y components that are present on every page:

- `SkipToMain` renders a visually-hidden link that becomes visible on keyboard focus. It targets `#main-content` and lets keyboard users skip the sidebar and topbar.
- `RouteAnnouncer` is an ARIA live region (`aria-live="polite"`) that announces the new page title after each navigation. This gives screen reader users feedback without a full page reload.

The `<main>` element has `id="main-content"` and `tabIndex={-1}` so the skip link can move focus to it programmatically. The `focus:outline-none` class removes the default focus ring on the main element itself (not on interactive elements inside it).

All interactive elements (sidebar links, buttons, palette items) use Radix UI primitives or shadcn components that implement WCAG 2.1 AA keyboard navigation patterns by default.

---

## Build and CI

The Tauri app is built with `pnpm tauri build` from the `apps/cascade-app/` directory. The frontend is bundled by Vite; Rust is compiled by `cargo` inside the Tauri CLI.

TypeScript checking runs separately via `pnpm run typecheck` (`tsc --noEmit`). The CI workflow runs typecheck, unit tests, and a pack-check on every push to main.

Development: `pnpm tauri dev` starts Vite's dev server and the Tauri process together. Hot module replacement works for the frontend. Rust changes require a full `cargo` recompile.
