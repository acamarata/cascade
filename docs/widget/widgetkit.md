# WidgetKit extension

`CascadeWidgetExtension` adds Cascade to the macOS widget gallery in three
sizes: small, medium and large. It is embedded in the menu bar app
(`CascadeMenuBar.app/Contents/PlugIns/CascadeWidgetExtension.appex`) and is
built from `apps/widget-macos/project.yml`.

## Input: the App Group file, nothing else

The extension reads one file: `widget-snapshot.json` in the App Group
container `$(TeamIdentifierPrefix)com.acamarata.cascade.widget`. The menu bar
app writes that file with the exact bytes `status.widget` returned (see
[feed.md](feed.md) and the snapshot privacy section of [README.md](README.md)).

- The extension is sandboxed. Its only other entitlement is the App Group.
- It opens no socket and makes no network request. Its sources import neither
  `Network` nor `URLSession`.
- It decodes the file with the same `WidgetModels.swift` the app uses. There is
  no second decoding model.
- It keeps no cache. Every timeline reads the file again.
- A missing, empty, corrupt or oversized (over 1 MiB) file gives a no-data
  entry: the em-dash in every value slot and no rows. Values from an earlier
  read never come back.
- Any read failure, including one caused by the file's complete file
  protection, gives the same no-data state.
- The widget shows no age for the snapshot. If the menu bar app stops, the last
  successfully written file stays on screen until it is removed or replaced.

## Timeline

Each timeline has one entry and the policy `.after(entry date + 30 s)`.
WidgetKit may throttle reloads; 30 s is the requested cadence, not a promise.
The menu bar app also calls `WidgetCenter.shared.reloadAllTimelines()` after
every successful snapshot write. A failed write invalidates the file and does
not request a reload. The placeholder shown in the gallery is the no-data
entry, so no sample values are ever drawn.

## Layouts

| Family | Shows |
|---|---|
| Small | title, first project label, its task summary |
| Medium | small, plus the attention count and up to four provider rows with five-hour and seven-day percentages |
| Large | medium, plus the project table, capped at five rows |

Any value the file lacks renders as the em-dash (`—`). A `null` window
percentage is the em-dash, never `0%`.

## Privacy of rendered text

Every string a widget draws is either a string from the snapshot file, the
display form of a number from the file, or one of these fixed literals:
`Cascade`, `Attention`, `5h`, `7d`, `Projects`, `—`. Labels are drawn verbatim.
A row the daemon labelled `redacted` stays `redacted`; the widget never shows
its ref or resolves a name. `WidgetPrivacyTests` enumerates the text of all three
layouts against the strings parsed from the file.

## Disclosure: project content

In this release the daemon always sends `projects: []` (see [feed.md](feed.md)).
The small layout's project label and task summary therefore show the em-dash,
and the large layout's project table is empty. The layouts are built and tested
against synthetic project fixtures, but project content on a real install is
**disclosed, not passed** (checklist item C6P). Real project rows arrive with
the daemon change tracked for the next phase.

## Build and test

```sh
xcodegen generate --spec apps/widget-macos/project.yml --project apps/widget-macos
xcodebuild -project apps/widget-macos/CascadeWidget.xcodeproj -scheme CascadeMenuBar \
  -destination 'platform=macOS' build
xcodebuild -project apps/widget-macos/CascadeWidget.xcodeproj -scheme CascadeWidgetExtensionTests \
  -destination 'platform=macOS' -enableThreadSanitizer YES test
```

The extension tests compile the extension sources with `WidgetModels.swift` and
`Formatting.swift` into a plain unit-test bundle, and read fixtures from
`CascadeWidgetExtensionTests/testdata`. Without a Developer ID identity in the
keychain, add `CODE_SIGN_IDENTITY=- DEVELOPMENT_TEAM=` for an ad-hoc signed
local build.
