# `cascade widget`

Manages the LaunchAgent that starts the macOS menu-bar widget at login. See
[installing the widget](../widget/install.md) for the walkthrough.

```
cascade widget install   [--json]
cascade widget uninstall [--json]
cascade widget status    [--json]
```

All three verbs are non-interactive. `install` and `uninstall` are
idempotent and exit 0 on a repeat run.

## `install`

Writes `~/Library/LaunchAgents/com.acamarata.cascade.widget.plist`, then runs
`launchctl bootout` (a failure is ignored) and `launchctl bootstrap` against
`gui/<uid>`.

| Result | Meaning |
|---|---|
| `installed` | first install |
| `reloaded` | the plist was already ours; it was rewritten and reloaded |

Errors:

| Kind | When |
|---|---|
| `not-found` | neither `/Applications/CascadeMenuBar.app` nor `~/Applications/CascadeMenuBar.app` exists, or the bundle found has no regular executable at `Contents/MacOS/CascadeMenuBar` |
| `invalid-input` | a bundle location is a symlink; it must be a real directory |
| `conflict` | a plist not written by cascade is already at the path |
| `unsupported` | not macOS |

`--json` prints the report with the install time:

```json
{
  "version": 1,
  "ok": true,
  "data": {
    "action": "installed",
    "detail": "widget launch agent installed at /Users/example/Library/LaunchAgents/com.acamarata.cascade.widget.plist",
    "installed_at": "2026-10-12T09:01:13Z"
  }
}
```

## `uninstall`

Runs `launchctl bootout` and removes the plist. With no plist it reports
`not installed` and exits 0. A plist not written by cascade is refused and
left in place.

## `status`

Reads the plist, asks `launchctl print gui/<uid>/com.acamarata.cascade.widget`
whether the agent is loaded, and reads the widget's own status file named by
`CASCADE_WIDGET_STATUS_PATH` in the plist.

`--json` fields:

| Field | Meaning |
|---|---|
| `installed` | the plist exists |
| `managed` | cascade wrote the plist; when false nothing else in the report is filled or trusted |
| `program_valid` | the plist runs exactly `Contents/MacOS/CascadeMenuBar` inside one of the two fixed bundle locations |
| `loaded` | launchd knows the agent |
| `state` | launchd's `state` line, such as `running` or `not running` |
| `plist_path` | where the plist is |
| `bundle_path` | the bundle the plist runs |
| `bundle_present` | that bundle exists on disk |
| `status_path` | where the widget is told to write its self-report |
| `self_report_error` | the self-report file exists but could not be read or parsed |
| `self_report` | `bundle_path`, `window_level`, `written_at`, `first_rows_at` from the widget |
| `placement` | `RUNNING_FROM_TRASH` or `MOVED` when the widget reports a bundle other than `bundle_path`; absent otherwise |

## Related

`cascade doctor --widget` runs only the widget health check. The default
`cascade doctor` report leaves it out. The check is OK when the widget is not
installed, warns on a missing bundle, `RUNNING_FROM_TRASH`, `MOVED`, an
unloaded agent, or a loaded agent whose widget has not written a readable
self-report, and errors on a plist cascade did not write, a plist whose
program is not the app, a window level below -1, or when a loaded widget
gets no `status.widget` answer from the daemon within 2 seconds.

`--widget` wins over the other narrowing flags: `cascade doctor --widget
--storage` runs only the widget check.
