# Cascade — macOS Native Widget

A WidgetKit widget for macOS 14+ that shows Claude Code project status.

## Requirements

- macOS 14.0+
- Xcode 15+
- cascade-dash installed with `install.sh`

## Data Source

The widget reads from the shared App Group container:
`~/Library/Group Containers/group.io.cascade/cache.json`

This file is written by the `io.cascade.refresh` LaunchAgent whenever it rebuilds the cache.

## Installing

See `SETUP.md` for the one-time Xcode project setup, then:

```bash
./build.sh
open build/Cascade.app
```

Then add the widget from the macOS widget gallery (right-click Desktop → Edit Widgets).

## Sizes

- **Small** — Active project + task count summary
- **Medium** — Project info + GCI stats + inbox/ideas badges
- **Large** — Medium content + 5-project table

## Dev Notes

The widget extension and companion app must share the same Team ID for the App Group
entitlement (`group.io.cascade`) to work. For local dev, use a free signing identity
or your personal team. The companion app (Cascade.app) is required as a container.
