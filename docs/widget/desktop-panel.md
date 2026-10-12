# Desktop panel, bundle-path guard and self-report

The macOS menu bar app (`apps/widget-macos`) adds three pieces on top of the status item and popover.

## Desktop fleet panel

`DesktopFleetWindow` is the unsigned fallback for the signed desktop widget, and its header says so.

| Property | Value |
|---|---|
| Window type | `NSPanel` with `.nonactivatingPanel`: clicks never activate the app |
| Level | `CGWindowLevelForKey(.normalWindow) - 1`: above the desktop, below every normal window |
| Spaces | `.canJoinAllSpaces`, `.stationary` (stays put during Mission Control and Space switches) |
| Moving | drag the background or the header strip (`isMovableByWindowBackground`) |
| Clicks | the hosting view accepts the first mouse click |
| Content | the popover's `FleetPopoverView`, one row per fleet row |

The panel is never a desktop-level window. On macOS 14 and later, "Click wallpaper to reveal desktop" sends every
click on a desktop-level window to the show-desktop gesture. It is never a normal-level window either, because it
would then cover work windows.

The panel is shown at launch. Right-click the `Cascade` status item for the menu: **Show/Hide Desktop Panel** toggles
it and **Quit Cascade** quits. A left click opens the popover as before.

## Bundle-path guard

At launch and on every poll interval (30 s) the app checks where its bundle is. It may run only from
`/Applications` or `~/Applications`, and never from a Trash (`~/.Trash`, `/Volumes/<disk>/.Trashes/<uid>`). The
check compares path components, so `/ApplicationsX` does not count as `/Applications`. The bundle is tracked
through a file reference, so moving the running app to the Trash is seen on the next poll.

When the check fails, the app shows no UI and:

1. relaunches the installed copy if one exists, trying `/Applications/CascadeMenuBar.app` first and then
   `~/Applications/CascadeMenuBar.app` (no other path is ever launched), then quits; or
2. shows one alert asking you to move the app into an Applications folder, then quits.

## Self-report

`cascade doctor --widget` reads a small JSON file the app writes. The app takes its path only from
`CASCADE_WIDGET_STATUS_PATH`. `cascade widget install` sets it to `<data dir>/widget/widget-status.json`, so the app
and doctor read the same file under any `HOME` or `CASCADE_HOME`. The app never derives this path from the home
directory. When the variable is unset or not absolute, no file is written and the popover shows one line naming
`cascade widget install`.

The file is replaced atomically, has mode `0600`, and its parent directory is created with mode `0700`. It holds
exactly these keys:

| Key | Type | Meaning |
|---|---|---|
| `bundle_path` | string | absolute path of the running app bundle |
| `window_level` | int | the desktop panel's window level |
| `written_at` | RFC 3339 | when this copy of the file was written |
| `first_rows_at` | RFC 3339 or null | when the first `status.widget` answer with at least one row arrived |

It is written at launch, whenever the panel level changes, and once when the first answer with rows arrives.
`first_rows_at` never changes after that, so the time from install to first row can be measured.
