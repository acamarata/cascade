# Windows Widget

This page documents the Cascade Windows Widget — a Windows App SDK 1.5 widget that
displays live daemon status on the Windows 11 Widget Board.

---

## Overview

The Cascade Windows Widget pins to the Windows 11 Widget Board and shows a real-time
summary of the cascade daemon state. It reads `~/.cascade/status-cache.json` directly
(no IPC) and refreshes every 30 seconds via a Windows thread-pool timer.

**App identity:** `dev.cascade.widget`
**Location:** `apps/cascade-widget-windows/CascadeWidget/`
**Build system:** MSBuild (C++/WinRT, Windows App SDK 1.5, x64)

---

## Architecture

### Components

| File | Purpose |
|---|---|
| `CascadeWidgetImpl.h/.cpp` | Per-tile state: StatusCache reader, poll timer, Adaptive Card renderer |
| `WidgetProvider.h/.cpp` | Routes Widget Board callbacks to per-tile `CascadeWidgetImpl` instances |
| `Package.appxmanifest` | MSIX package manifest; registers the widget extension |
| `assets/cascade_widget_medium.json` | Adaptive Card 1.5 template (4×2 grid) |
| `assets/cascade_widget_small.json` | Adaptive Card 1.5 template (2×2 grid) |

### Data Flow

```
cascaded daemon
    │
    │ writes every 30s
    ▼
~/.cascade/status-cache.json
    │
    │ CreateFileW (FILE_SHARE_READ|FILE_SHARE_WRITE)
    ▼
CascadeWidgetImpl::ReadCacheFile()
    │
    │ Windows.Data.Json::TryParse
    ▼
CascadeStatus struct (m_lastState)
    │
    │ WidgetManager::UpdateWidget
    ▼
Windows Widget Board (Adaptive Card)
```

---

## StatusCache Schema

The widget reads fields from the daemon's `status-cache.json` schema (owned by E-02,
frozen after T-P2-E02-04):

| JSON field | Type | Widget mapping |
|---|---|---|
| `tier_counts` | object (string→int) | Sum of all values → `tierCountsTotal` |
| `rag_freshness_secs` | number | `< 300` → "fresh"; otherwise "stale (Xs)" |
| `active_agents` | number | `activeAgents` int |
| `inbox_unread` | number | `inboxUnread` int |
| `daemon_status` | string | `daemonStatus` (e.g. "Running", "Stopped") |

---

## Polling

- Poll interval: **30 seconds** via `SetThreadpoolTimer` (Windows thread-pool timer, not a Sleep loop)
- First read: **immediate** on `Activate()` — the tile shows live data without a 30-second delay
- Thread safety: `m_lastState` guarded by `m_stateMutex`; `UpdateWidget` is COM-safe cross-thread
- Error handling: malformed or absent cache JSON retains the previous `m_lastState` (last-known-good)

---

## File Access

`ReadCacheFile()` opens `~/.cascade/status-cache.json` using `CreateFileW` with:

```cpp
CreateFileW(
    m_cachePath.c_str(),
    GENERIC_READ,
    FILE_SHARE_READ | FILE_SHARE_WRITE,   // allows concurrent daemon writes
    nullptr,
    OPEN_EXISTING,
    FILE_ATTRIBUTE_NORMAL,
    nullptr);
```

The `FILE_SHARE_WRITE` flag prevents `ERROR_SHARING_VIOLATION` when the daemon holds
the file open for writing. `std::ifstream` omits this flag and is not used here.

---

## Adaptive Card Template

The widget uses Adaptive Card Template Language `${...}` bindings. The medium template
(4×2 grid) displays:

- Daemon status with colour coding (`Good` when "Running", `Warning` otherwise)
- Total tier-counts rule count (large number)
- Active agent count
- Unread inbox count
- RAG freshness string

The template is embedded as a compile-time `wstring_view` constant in
`CascadeWidgetImpl.cpp` at P2 scope. P3 can add a file-system watcher for hot-reload.

---

## Build

**Platform:** Windows only (C++/WinRT, Windows App SDK 1.5)

```
msbuild CascadeWidget.sln /restore /t:Build /p:Configuration=Release /p:Platform=x64
```

**Dependencies:**
- Windows App SDK 1.5 (NuGet: `Microsoft.WindowsAppSDK`)
- C++/WinRT projection generator (NuGet: `Microsoft.Windows.CppWinRT`)
- WIL — Windows Implementation Library (NuGet: `Microsoft.Windows.ImplementationLibrary`)

**Status:** P2 implementation complete; build infra (T-P2-E06-07) requires remediation
of two CRITICAL defects (dual NuGet restore mode; WinRT type model) before MSBuild exits 0.

---

## Sizes Supported

| Size | Grid | Content |
|---|---|---|
| Small | 2×2 | App name + daemon online/offline indicator |
| Medium | 4×2 | Full: daemon status, tier count, agents, inbox, RAG freshness |

---

## Future Work (P3)

- Adaptive Card template hot-reload (file-system watcher on `assets/`)
- Per-tier breakdown in FactSet rows (currently all set to 0)
- Customisation panel (`OnWidgetCustomizationRequested`)
- MSIX code signing (T-P2-E06-14)
