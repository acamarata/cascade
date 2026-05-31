# Cascade — macOS Native Widget: Xcode Project Setup

The Swift source files are ready. You need to create the Xcode project manually
(the `project.pbxproj` format is too complex to generate by hand).

## One-time setup (10 minutes)

### 1. Create the container app

1. Open Xcode → File → New → Project
2. Choose: macOS → App
3. Name: `CascadeApp`, Bundle ID: `io.cascade.app`
4. Language: Swift, Interface: SwiftUI
5. Save into this directory: `src/widget/macos/`

### 2. Add the widget extension

1. File → New → Target
2. Choose: macOS → Widget Extension
3. Name: `CascadeWidgetExtension`
4. Bundle ID: `io.cascade.widgetextension`
5. Uncheck "Include Configuration Intent" (static widget)
6. Activate the new scheme when prompted

### 3. Enable the App Group on both targets

For each target (CascadeApp and CascadeWidgetExtension):

1. Select the target → Signing & Capabilities
2. Click "+ Capability" → App Groups
3. Add group: `group.io.cascade`

This allows the widget to read `cache.json` from the shared container.

### 4. Replace the generated Swift files

Delete Xcode's auto-generated stubs and replace with the files in this directory:

**CascadeWidgetExtension target — add these files:**
- `CascadeWidgetExtension/CacheModel.swift`
- `CascadeWidgetExtension/Entry.swift`
- `CascadeWidgetExtension/Provider.swift`
- `CascadeWidgetExtension/CascadeWidget.swift`
- `CascadeWidgetExtension/CascadeWidgetBundle.swift`
- `CascadeWidgetExtension/Views/EntryView.swift`
- `CascadeWidgetExtension/Views/SmallView.swift`
- `CascadeWidgetExtension/Views/MediumView.swift`
- `CascadeWidgetExtension/Views/LargeView.swift`

**CascadeApp target — add these files:**
- `CascadeApp/CascadeAppApp.swift`
- `CascadeApp/ContentView.swift`

Note: `CacheModel.swift` is referenced by both ContentView and the extension.
Add it to both targets in Xcode (select the file, check both targets in the
File Inspector on the right panel).

### 5. Set deployment target

Both targets: macOS 14.0+

### 6. Build and run

```bash
./build.sh
# or just press Cmd+R in Xcode with CascadeApp scheme selected
```

Then open Cascade.app, right-click the Desktop → Edit Widgets, and add the widget.

## Data flow

```
cascade-dash LaunchAgent
  → writes ~/Library/Group Containers/group.io.cascade/cache.json
  → WidgetKit reads it every 5 minutes via CascadeProvider
  → renders SmallView / MediumView / LargeView
```

## Troubleshooting

**Widget shows placeholder data:** the App Group entitlement is not enabled or the
cache file doesn't exist yet. Run `cascade-dash refresh` to create the cache.

**"group.io.cascade" container not found:** both targets need the App Group capability
enabled and signed with the same Team ID.

**Widget doesn't update:** WidgetKit on macOS can throttle refresh frequency.
For immediate refresh during dev, run `WidgetKit Simulator` or use
`notifyWidgetKitSimulator` from the cascade-dash CLI.
