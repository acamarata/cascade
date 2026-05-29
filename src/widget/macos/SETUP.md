# Claw Dash — macOS Native Widget: Xcode Project Setup

The Swift source files are ready. You need to create the Xcode project manually
(the `project.pbxproj` format is too complex to generate by hand).

## One-time setup (10 minutes)

### 1. Create the container app

1. Open Xcode → File → New → Project
2. Choose: macOS → App
3. Name: `ClawDashApp`, Bundle ID: `io.clawdash.app`
4. Language: Swift, Interface: SwiftUI
5. Save into this directory: `src/widget/macos/`

### 2. Add the widget extension

1. File → New → Target
2. Choose: macOS → Widget Extension
3. Name: `ClawDashWidgetExtension`
4. Bundle ID: `io.clawdash.widgetextension`
5. Uncheck "Include Configuration Intent" (static widget)
6. Activate the new scheme when prompted

### 3. Enable the App Group on both targets

For each target (ClawDashApp and ClawDashWidgetExtension):

1. Select the target → Signing & Capabilities
2. Click "+ Capability" → App Groups
3. Add group: `group.io.clawdash`

This allows the widget to read `cache.json` from the shared container.

### 4. Replace the generated Swift files

Delete Xcode's auto-generated stubs and replace with the files in this directory:

**ClawDashWidgetExtension target — add these files:**
- `ClawDashWidgetExtension/CacheModel.swift`
- `ClawDashWidgetExtension/Entry.swift`
- `ClawDashWidgetExtension/Provider.swift`
- `ClawDashWidgetExtension/ClawDashWidget.swift`
- `ClawDashWidgetExtension/ClawDashWidgetBundle.swift`
- `ClawDashWidgetExtension/Views/EntryView.swift`
- `ClawDashWidgetExtension/Views/SmallView.swift`
- `ClawDashWidgetExtension/Views/MediumView.swift`
- `ClawDashWidgetExtension/Views/LargeView.swift`

**ClawDashApp target — add these files:**
- `ClawDashApp/ClawDashAppApp.swift`
- `ClawDashApp/ContentView.swift`

Note: `CacheModel.swift` is referenced by both ContentView and the extension.
Add it to both targets in Xcode (select the file, check both targets in the
File Inspector on the right panel).

### 5. Set deployment target

Both targets: macOS 14.0+

### 6. Build and run

```bash
./build.sh
# or just press Cmd+R in Xcode with ClawDashApp scheme selected
```

Then open ClawDash.app, right-click the Desktop → Edit Widgets, and add the widget.

## Data flow

```
claw-dash LaunchAgent
  → writes ~/Library/Group Containers/group.io.clawdash/cache.json
  → WidgetKit reads it every 5 minutes via ClawDashProvider
  → renders SmallView / MediumView / LargeView
```

## Troubleshooting

**Widget shows placeholder data:** the App Group entitlement is not enabled or the
cache file doesn't exist yet. Run `claw-dash refresh` to create the cache.

**"group.io.clawdash" container not found:** both targets need the App Group capability
enabled and signed with the same Team ID.

**Widget doesn't update:** WidgetKit on macOS can throttle refresh frequency.
For immediate refresh during dev, run `WidgetKit Simulator` or use
`notifyWidgetKitSimulator` from the claw-dash CLI.
