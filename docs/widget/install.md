# Installing the menu-bar widget

The menu-bar widget is a macOS app, `CascadeMenuBar.app`. `cascade widget
install` registers a LaunchAgent so it starts at login and talks to your
cascade daemon.

## Before you install

Put `CascadeMenuBar.app` in one of two places:

1. `/Applications/CascadeMenuBar.app`
2. `~/Applications/CascadeMenuBar.app`

The first one that exists is used. These two paths are fixed. No environment
variable changes them, and cascade never opens the app by name. If neither
exists, install fails and names both paths. A location that is a symlink, or
a bundle without a regular executable at `Contents/MacOS/CascadeMenuBar`, is
refused.

## Install

```
cascade widget install
```

This writes `~/Library/LaunchAgents/com.acamarata.cascade.widget.plist` and
loads it into your `gui/<uid>` launchd domain (a `bootout`, then a
`bootstrap`). The agent runs `CascadeMenuBar.app/Contents/MacOS/CascadeMenuBar`
by its absolute path.

Install records four environment variables for the widget:

| Variable | Value |
|---|---|
| `CASCADE_CLI_PATH` | the cascade binary you ran, with symlinks resolved |
| `CASCADE_SOCKET_PATH` | the daemon socket |
| `CASCADE_WIDGET_STATUS_PATH` | `<data dir>/widget/widget-status.json` |
| `CASCADE_HOME` | only when it is set in the shell that ran install |

Run install again at any time. It rewrites the same plist, reloads the agent
and reports `reloaded`. If a file you did not get from cascade already sits at
the plist path, install refuses and leaves it alone.

## Check it

```
cascade widget status
cascade doctor --widget
```

`status` says whether the agent is installed and loaded, where the bundle is,
and what the running widget reports about itself. It flags two problems:

- `RUNNING_FROM_TRASH`: the widget is running from a bundle in the Trash.
- `MOVED`: the widget runs from a different path than the one you installed.

Both paths are shown. Move the app back, or run `cascade widget install` again.

`status` and `doctor` also refuse to vouch for a launch agent cascade did not
write, or one whose program is not the app. Inspect that file and remove it.
`cascade doctor --widget` runs the same checks plus a daemon probe, and
`cascade doctor` leaves the widget check out of its default report.

## Remove it

```
cascade widget uninstall
```

This unloads the agent and deletes the plist. Running it again reports
`not installed` and exits 0. The app itself is not touched.

## Other platforms

The widget exists on macOS only. On Linux and Windows every `cascade widget`
verb exits with an unsupported error that names the verb.
