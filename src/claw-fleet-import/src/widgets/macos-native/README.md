# claw-fleet — macOS Native

Two native macOS surfaces for the claw-fleet usage monitor. Both read
`~/.claude/usage-cache.json`, which is written by the claw-fleet-refresh
daemon and updated every 5 minutes.

## Surfaces

### ClawFleetMenuBar — real-time menu bar app + desktop widget

A `MenuBarExtra` app (macOS 13+, SwiftUI App lifecycle). It polls the cache
file every 60 seconds and renders the full account table in a click-to-open
panel. The menu bar title shows available-out-of-total (`4/9`, `0/9`, etc.).

This is the authoritative live surface. Data is at most 60 seconds old.

The app also spawns a **desktop widget window** at launch: a borderless,
semi-transparent panel that floats on the desktop layer (below all app windows,
above wallpaper) and shows the same fleet table. It is visible across all
Spaces, drags freely by clicking anywhere on its background, and does not steal
focus from the foreground app.

#### Desktop widget — toggling

Open the menu bar panel and click **Desktop widget** in the footer. A filled
circle icon indicates the widget is currently shown; clicking toggles it. The
preference persists across launches.

#### Desktop widget — position

The widget defaults to the top-right corner of the primary display (24 px
inset). Drag it anywhere; the position is saved to
`~/.config/claw-fleet/desktop-window-position.json` and restored on the next
launch.

Show/hide state is saved to `~/.config/claw-fleet/desktop-window-visible.json`.

### ClawFleetWidget — WidgetKit (inert without a Developer cert)

A WidgetKit extension (.systemSmall + .systemMedium). It reads the same cache
file but is refreshed on the system's schedule, not on demand. In practice
WidgetKit throttles updates to roughly every 15-60 minutes depending on device
state and battery. Treat it as a snapshot, not a live view.

The widget is intentionally lightweight: label, week%, and session% per account.
Use the menu bar app or the desktop widget when you need current data.

**Note:** WidgetKit extensions require a real Apple Developer certificate to
register with the system widget gallery. Ad-hoc signing is rejected by the
extension host. The `ClawFleetWidget` target compiles cleanly but is inert
without a team cert. The desktop window provides the same glanceable view
without needing WidgetKit or a developer account.

## Architecture

```
Sources/
  Shared/
    UsageCache.swift            Codable models + file loader (expands ~ via FileManager)
    Formatting.swift            Color thresholds, label logic, time formatting — ported from index.jsx
    UsageRow.swift              SwiftUI row + header views shared by both targets
  MenuBar/
    ClawFleetApp.swift          @main App, MenuBarExtra scene, DesktopManager + DesktopVisibilityStore
    CacheStore.swift            @MainActor ObservableObject: 60s poll loop, cache state
    MenuBarLabel.swift          The menu bar title text (MenuBarLabelView)
    MenuBarContentView.swift    The click-to-open panel: table + refresh + desktop toggle + quit
    DesktopWindowController.swift  NSPanel lifecycle: create, show, hide, drag-persist position
    DesktopFleetView.swift      SwiftUI content for the desktop panel (dark blur background)
    Info.plist                  LSUIElement=true (no Dock icon)
  Widget/
    ClawFleetWidget.swift       WidgetBundle + Widget configuration
    CacheTimelineProvider.swift TimelineProvider: 15-min entries, .after(1h) policy
    ClawFleetWidgetView.swift   Small + Medium layout views
    Info.plist
```

## Non-sandbox rationale

Both targets run without App Sandbox (`ENABLE_APP_SANDBOX = NO`). The reason
is straightforward: the cache file lives at `~/.claude/usage-cache.json`, an
absolute path under the user's home directory that is not in any app-group
container. App Sandbox restricts file access to the app's own container and
explicit user-granted bookmarks. This is a single-user personal tool, not a
Mac App Store submission, so the sandbox restriction provides no benefit and
would require a Security-Scoped Bookmark flow that adds complexity with no
practical value.

If you intend to distribute this on the Mac App Store, you would need to:
1. Re-enable `ENABLE_APP_SANDBOX = YES` on both targets.
2. Either use an App Group to share a container, or prompt the user with
   `NSOpenPanel` to select the file and persist a security-scoped bookmark.

## Build and run (no Apple Developer account required)

The project is configured for ad-hoc local signing. You do not need an Apple
Developer certificate to build and run the menu bar app on your own machine.

### Requirements

- macOS 13.0+
- Xcode 16+ (tested on Xcode 26.3 / Swift 6.2)
- XcodeGen: `brew install xcodegen`

### One-time build

```sh
cd src/widgets/macos-native
xcodegen generate
xcodebuild -project ClawFleet.xcodeproj -scheme ClawFleetMenuBar \
  -configuration Release -derivedDataPath build \
  CODE_SIGN_IDENTITY="-" CODE_SIGN_STYLE=Manual \
  CODE_SIGNING_REQUIRED=NO CODE_SIGNING_ALLOWED=YES \
  ENABLE_HARDENED_RUNTIME=NO ONLY_ACTIVE_ARCH=YES build
```

### Install and launch

```sh
# Install to ~/Applications
APP=$(find build -name "ClawFleet.app" -type d | head -1)
cp -R "$APP" ~/Applications/ClawFleet.app
xattr -cr ~/Applications/ClawFleet.app   # clear quarantine if needed
open ~/Applications/ClawFleet.app
```

The menu bar icon appears immediately and shows available/total (e.g. `4/9`).
Clicking it opens the full account table. The app has no Dock icon (`LSUIElement`).

### Auto-start on login (LaunchAgent)

A LaunchAgent plist is installed at
`~/Library/LaunchAgents/com.acamarata.clawfleet.menubar.plist` and loaded
automatically by the `install.sh` helper. To manage it manually:

```sh
# Load (enable auto-start)
launchctl load ~/Library/LaunchAgents/com.acamarata.clawfleet.menubar.plist

# Unload (disable auto-start, does not kill the running process)
launchctl unload ~/Library/LaunchAgents/com.acamarata.clawfleet.menubar.plist

# Check status
launchctl list | grep clawfleet.menubar

# Kill the running process
pkill -x ClawFleet
```

### Signing reality

Both targets use ad-hoc signing (`CODE_SIGN_IDENTITY="-"`). Gatekeeper will
not block an app you built and installed yourself from `~/Applications/`.
If macOS shows "damaged or incomplete" after a `cp`, run `xattr -cr` on the
bundle to clear the quarantine attribute.

## Known limitations

### ClawFleetWidget (WidgetKit extension — requires Developer cert)

The WidgetKit extension (`ClawFleetWidget`) **compiles cleanly** but **will not
load in the widget gallery** without a valid Apple Developer certificate.
WidgetKit extensions must be signed with a real team identity for the system to
register them. Ad-hoc signing is rejected by the extension host.

The **desktop widget window** (built into `ClawFleetMenuBar`) is the
WidgetKit-free alternative. It shows the same live fleet table, floats on the
desktop layer across all Spaces, and works fully under ad-hoc signing without
any developer account.

To load the WidgetKit widget you would need:
1. A free or paid Apple Developer account.
2. Set `CODE_SIGN_STYLE: Automatic` and your team ID in project.yml.
3. Build via Xcode with your team selected.

## Color reference

Utilization thresholds match the Übersicht widget exactly:

| Range | Color |
|-------|-------|
| null / unknown | #6B7280 (gray) |
| >= 100% | #9CA3AF (muted) |
| >= 76% | #E5484D (red) |
| >= 51% | #F76808 (orange) |
| >= 26% | #F5A623 (amber) |
| < 26% | #30A46C (green) |

Row opacity: weekly cap → 0.28, 5-hour cap → 0.6, otherwise 1.0.

Stale marker (`*`): last_pull_at older than 30 minutes AND not in back-off.
