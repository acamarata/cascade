# cascade-widget-macos

macOS WidgetKit extension displaying live Cascade fleet status.

Sizes: small (158×158), medium (338×158), large (338×354).

## Requirements

- Xcode 15+
- macOS 13 (Ventura)+
- Swift 5.9+
- The Cascade main app running (writes daemon-status.json via App Group)

## Architecture

```
CascadeWidget.swift         — widget bundle, TimelineProvider, entry point
CascadeStatus.swift         — model; reads shared App Group JSON from daemon
Intent.swift                — AppIntents configuration (agent group + relay toggle)
SmallWidgetView.swift       — 158×158 face
MediumWidgetView.swift      — 338×158 face
LargeWidgetView.swift       — 338×354 face
```

The widget reads `daemon-status.json` from the shared App Group container
`group.com.acamarata.cascade`. The Tauri main process writes this file every
~15 seconds. If the file is missing or older than 90 seconds the widget shows
the daemon as offline.

## IPC flow

```
Tauri daemon process
  → writes ~/Library/Group Containers/group.com.acamarata.cascade/daemon-status.json
      ↑ every 15 s

WidgetKit extension (sandboxed)
  → reads the same file via FileManager.containerURL(forSecurityApplicationGroupIdentifier:)
  → refreshes timeline on 30-second intervals
```

## Build

Open the Xcode project in `apps/cascade-app` (the Tauri Xcode wrapper). The widget
extension target `CascadeWidget` is already added as a linked extension.

Manual SPM build (for CI / testing only — WidgetKit extensions must be embedded in a host app):

```bash
cd apps/cascade-widget-macos
swift build
```

## Adding to Xcode

1. In Xcode, open the main Cascade project.
2. File → New Target → Widget Extension → set name `CascadeWidget`.
3. Delete Xcode-generated stubs and point the target's source folder at `Sources/CascadeWidget/`.
4. Add `group.com.acamarata.cascade` to App Groups entitlement on **both** the main app target
   and the widget extension target.
5. Build and run the main app — the widget appears in the macOS widget picker.

## JSON schema (daemon-status.json)

```json
{
  "updatedAt": "2026-05-30T12:00:00Z",
  "daemonAlive": true,
  "relayAlive": true,
  "activeRequests": 3,
  "agents": [
    {
      "id": "A1",
      "provider": "Anthropic",
      "tier": "T2",
      "usagePct": 0.32,
      "quotaLabel": "32 % · resets 02:14",
      "isActive": true,
      "color": "#E87040"
    }
  ]
}
```

All dates are ISO-8601 UTC. `usagePct` is 0.0–1.0.
