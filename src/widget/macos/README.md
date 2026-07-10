# Cascade macOS Native Widget

A WidgetKit widget for macOS 14+ that shows Cascade project and daemon status.

## Requirements

- macOS 14.0+
- Xcode 15+
- cascade daemon (`cascaded`) running, writing the shared cache

## Data Source

The widget reads from the shared App Group container:
`~/Library/Group Containers/group.io.cascade/cache.json`

This file is written by the cascade daemon whenever it rebuilds the cache.

## Installing

Install [xcodegen](https://github.com/yonaskolb/XcodeGen) (`brew install xcodegen`), which
generates `CascadeWidget.xcodeproj` from `project.yml`. See `SETUP.md` for the full one-time
setup, then:

```bash
xcodegen generate
./build.sh
open ~/Applications/CascadeApp.app
```

Then add the widget from the macOS widget gallery (right-click Desktop, choose Edit Widgets).

## Menu-Bar App

`build.sh` installs the companion menu-bar app at `~/Applications/CascadeApp.app`.
The app reads account quota data from `~/.cascade/accounts/quota.json`, falling
back to `~/.claude/usage-cache.json` when needed. The desktop widget reads its
WidgetKit cache from `~/Library/Group Containers/group.io.cascade/cache.json`.

## Sizes

- **Small:** active project plus task-count summary
- **Medium:** project info, tier stats, inbox/ideas badges
- **Large:** medium content plus a 5-project table

## Dev Notes

The widget extension and companion app must share the same Team ID for the App Group
entitlement (`group.io.cascade`) to work. For local dev, use a free signing identity
or your personal team. The companion app (Cascade.app) is required as a container.
