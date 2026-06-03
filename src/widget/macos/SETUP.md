# Cascade macOS Native Widget: Xcode Project Setup

The Swift source files and xcodegen project spec are ready. The Xcode project is generated
from `project.yml` using [xcodegen](https://github.com/yonaskolb/XcodeGen).

## One-time setup

### 1. Install xcodegen (if not already installed)

```bash
brew install xcodegen
```

### 2. Generate the Xcode project

```bash
cd src/widget/macos
xcodegen generate
```

This creates `CascadeWidget.xcodeproj` from `project.yml`. The project includes two targets:

- **CascadeApp**: macOS container app (deployment target: 14.0)
- **CascadeWidgetExtension**: WidgetKit extension (deployment target: 14.0)

Both targets use App Group `group.io.cascade` for shared container access. The entitlement
files (`CascadeApp/CascadeApp.entitlements` and
`CascadeWidgetExtension/CascadeWidgetExtension.entitlements`) are generated automatically
by xcodegen from `project.yml`.

### 3. Build and run

```bash
./build.sh
# or press Cmd+R in Xcode with CascadeApp scheme selected
```

Then open `CascadeApp.app`, right-click the Desktop, choose Edit Widgets, and add the widget.

## Project structure

```
src/widget/macos/
├── project.yml                          # xcodegen spec. Edit this, not the .xcodeproj
├── CascadeWidget.xcodeproj/             # generated. Do not edit by hand
├── CascadeApp/
│   ├── CascadeApp.entitlements          # App Group group.io.cascade (generated)
│   ├── CascadeAppApp.swift
│   ├── ContentView.swift
│   └── Info.plist
├── CascadeWidgetExtension/
│   ├── CascadeWidgetExtension.entitlements  # App Group group.io.cascade (generated)
│   ├── CacheModel.swift
│   ├── Entry.swift
│   ├── Provider.swift
│   ├── CascadeWidget.swift
│   ├── CascadeWidgetBundle.swift
│   ├── Info.plist
│   └── Views/
├── build.sh
└── ExportOptions.plist
```

## Data flow

```
cascade daemon
  → writes ~/Library/Group Containers/group.io.cascade/cache.json
  → WidgetKit reads it every 5 minutes via CascadeWidgetProvider
  → renders SmallView / MediumView / LargeView
```

## Troubleshooting

**Widget shows placeholder data:** the App Group entitlement is not enabled or the
cache file does not exist yet. Run `cascade refresh` to create the cache.

**"group.io.cascade" container not found:** both targets need the App Group capability
enabled and signed with the same Team ID. For local dev, ad-hoc signing (`-`) is
configured in `project.yml`. No provisioning profile required.

**Widget does not update:** WidgetKit on macOS can throttle refresh frequency.
For immediate refresh during dev, use `notifyWidgetKitSimulator` or run the
WidgetKit Simulator in Xcode's Debug menu.

**Regenerating the project:** if you change `project.yml`, re-run `xcodegen generate`
from `src/widget/macos/`. The entitlement files are regenerated automatically.

## Apple Developer Account (Optional, for Distribution Only)

Local development works with ad-hoc signing (no Apple Developer account needed):
```bash
xcodebuild ... CODE_SIGN_IDENTITY="-" CODE_SIGNING_REQUIRED=NO
```

For distributable builds you need:
- Apple Developer account at developer.apple.com ($99/year)
- Create App Group "group.io.cascade" in Identifiers > App Groups
- Create App IDs "io.cascade.app" and "io.cascade.app.widgetextension"
- Download provisioning profiles and add to Xcode targets
- Run: `xcodebuild ... -allowProvisioningUpdates`

This step requires your Apple ID and is NOT automated.
See: https://developer.apple.com/documentation/xcode/configuring-app-groups
