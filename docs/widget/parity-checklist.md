# Widget parity checklist

The release candidate's menu-bar widget is accepted by a journal, not by a
statement. The journal lists 23 locators. Each row names the evidence behind
it, the release candidate (rc) tag and the sha256 of the installed artifact. A
Go verifier reads the journal, recomputes what it can and is red on every
missing, failed, stale or foreign row.

The session that runs this checklist on the real release candidate is
P1-EXIT-11, with the owner present. This page splits the work in two: the
build session prepares everything that needs no person at the keyboard (section
1); the owner performs the GUI steps (section 2) and the build session captures
the evidence (section 3).

Files:

- Template: `internal/acceptance/testdata/widget_parity_journal.template.md`
- Verifier and its seeded tests: `internal/acceptance/widget_parity_journal_test.go`
- Live check: `internal/acceptance/widget_parity_journal_live_test.go` (build tag `widgetparity`)

## The journal

One row per locator, exactly these 23:

`C1`, `C2`, `C3`, `C4`, `C4B`, `C5`, `C6`, `C6P`, `C7`, `C8`, `C10`, `C11`,
`SB1`, `SB2`, `SB3`, `SB4`, `FRESH-INSTALL`, `SIGNING`, `PAR-A`, `PAR-CO`,
`PAR-GP`, `PAR-OC`, `PAR-ZA`

```
| WIDGET/<locator> | passed|failed|disclosed-not-passed|deferred-P2 | <evidence path> | <rc tag> | <artifact sha256> |
```

| Rule | What the verifier does |
|---|---|
| Locator set | A missing row, a duplicate row or an unknown locator is red. |
| Status | `failed` is red. Any other word is red. |
| `disclosed-not-passed` | Valid only for `C6P`, and only when its evidence cites `DEF-P1-widget-project-rows`. |
| `deferred-P2` | Valid only for `PAR-CO`, `PAR-GP`, `PAR-OC`, `PAR-ZA`, and only when the evidence is a captured v1 feed row (section 4). `PAR-A` must be `passed`. A `failed` row is never downgraded. |
| Evidence | The path is relative to the journal's directory, must stay inside it and must name a non-empty file. The journal itself, the artifact, and a file another locator already cites are refused. |
| rc tag | Must equal the rc under test. An empty tag never matches. |
| Artifact sha256 | The verifier hashes the artifact itself and compares. A `.dmg` is hashed whole; a `.app` is hashed through `Contents/MacOS/CascadeMenuBar`. |
| `SIGNING` | The evidence is a directory with `codesign.txt`, `spctl.txt` and `notarization.txt`. The verifier runs `codesign` and `spctl` on the artifact itself: the artifact must carry a Developer ID Application authority (ad-hoc is red) and Gatekeeper must accept it with source `Notarized Developer ID`. The CDHash in `codesign.txt` must equal the artifact's own, and `spctl.txt` must read the same way (first line accepted, no rejected line). `notarization.txt` must carry a submission id and status Accepted and must not mention a skipped step. |
| `FRESH-INSTALL` | The evidence is a directory with `install.json` and `widget-status.json`. `first_rows_at` minus `installed_at` must be 0 to 30 seconds. A null `first_rows_at`, a missing install record or a first-rows time before the install is red. |

The report lists three disjoint groups: passed rows, `disclosed-not-passed`
rows and `deferred-P2` rows. A deferred row is never in the passed group.

## 1. Build-session preparation

No step here needs the owner, except the revocation in step 5.

1. **Fresh account.** Use a fresh macOS user account on the build Mac, or a
   fresh-HOME leg in the build session's GUI domain. Start with an empty `HOME`
   and an empty `CASCADE_HOME`. Never use the login keychain of a working
   account.
2. **Artifact.** Take the release candidate's signed, notarized `.dmg` and
   install it. Note the rc tag and the artifact path. Compute the hash the
   verifier will compare:

   ```
   shasum -a 256 CascadeMenuBar.dmg
   shasum -a 256 /Applications/CascadeMenuBar.app/Contents/MacOS/CascadeMenuBar
   ```

3. **Daemon.** Start `cascade daemon run` from the fresh `HOME`.
4. **Real providers.** Enrol at least one subscription account and one
   API-key provider as described in the [provider guide](../provider-guide.md).
   Leave one provider without a quota source for `C5`.
5. **A key provider driven to auth-required.** The build session never
   rotates or revokes a credential. The owner revokes the key at the provider.
   Then:
   - A revoked Anthropic or OpenAI-compatible key answers `401`. A revoked
     Gemini key answers `400` with `API_KEY_INVALID`; record that answer too.
   - A human issues the grant the conductor call needs:
     `cascade vault grant provider.<name>.key --ttl 1h`.
   - One conductor call, `cascade run --task <class>`, makes the daemon see
     the `401`. That call, not a flag, turns the row auth-required. The
     widget evidence ticket (P1-WID-11) records it.
6. **Journal skeleton.** Copy the template next to an `evidence/` directory.
   Fill the rc tag and the artifact sha256 in every row, and set `PAR-A` to
   `passed` once the owner has confirmed it. Keep the four deferred `PAR-*` rows
   and `C6P` as the template gives them.

## 2. The owner's GUI steps

The owner performs each step on the fresh account with the installed artifact,
and the build session records the result as evidence.

| Locator | Owner step | Expected result |
|---|---|---|
| `C1` | Open the popover and the desktop fleet window. Count the rows against `cascade provider list`. | One row per enrolled provider profile or pool, in both places. |
| `C2` | Read both windows of every row. | A percentage or an em-dash, and a countdown or an em-dash. No made-up number. |
| `C3` | Read the "updated N ago" label. Stop the daemon for a minute, then read it again. | The label follows the snapshot time and keeps ageing while reads fail. |
| `C4` | Click the amber row of the revoked key provider and confirm. | Terminal opens and runs `cascade provider reauth <ref>` for that provider. For a key provider the answer is the remedy line, `cascade provider reauth <ref> --key-env <VAR>`; the key is never read from the terminal. |
| `C4B` | Expire the primary credential entry but keep its refresh token, with no live CLI process. Wait one usage poll. | The row reads ok again with fresh numbers and never shows re-auth. Only after the refresh token is revoked does amber appear. |
| `C5` | Look at the provider that has no quota source. | Em-dashes, never a zero. |
| `C6` | Add the small, medium and large widgets. | Three distinct layouts on a 30 second timeline. |
| `C6P` | None. | Recorded as `disclosed-not-passed` (section 5). |
| `C7` | Run the four commands in section 3. | Install, status, uninstall and a second uninstall converge. |
| `C8` | Change a provider's state from another process. | The popover shows it within 30 seconds. |
| `C10` | Set "Click wallpaper to reveal desktop" to Always. Click Refresh, then drag the header. | Refresh fires and the panel moves. |
| `C11` | Move the running bundle to the Trash. | The guard fires within one poll; `cascade doctor --widget` reports `RUNNING_FROM_TRASH`. |
| `SB1` | Kill the SSE connection. | A poll delivers the next change within 30 seconds. |
| `SB2` | Push an attention item. | A badge shows in the popover header within one daemon tick. |
| `SB3` | Enrol a node. | Its presence state shows in the popover or the desktop window. |
| `SB4` | Lease a job. | The active jobs count follows it. An em-dash appears only when the daemon has no jobs domain. |
| `FRESH-INSTALL` | Install on the fresh account and watch for the first rows. | First rows within 30 seconds of the install. |
| `SIGNING` | None. | Recorded from tool output (section 3). |
| `PAR-A` | Read each subscription account row. | Live 5H and Wk values and the weekly reset. Needs re-auth shows only for a proven-dead credential. |

## 3. Evidence capture

Each evidence file is a non-empty file under `evidence/`. For a GUI step, write
what was seen and when into `evidence/<locator>.txt` and keep any screenshot
(`screencapture -x evidence/<locator>.png`) beside it.

```
cascade widget install --json > evidence/fresh-install/install.json
cp "<data dir>/widget/widget-status.json" evidence/fresh-install/widget-status.json

{ cascade widget install; cascade widget status --json; cascade widget uninstall; cascade widget uninstall; } > evidence/c7.txt 2>&1
cascade doctor --widget > evidence/c11.txt 2>&1

codesign -dv --verbose=4 <artifact> > evidence/signing/codesign.txt 2>&1
spctl -a -vv <artifact> > evidence/signing/spctl.txt 2>&1
```

For a `.dmg` use `spctl -a -vv -t open --context context:primary-signature
<artifact>` so Gatekeeper assesses the image as an installer. Save the
`notarytool` output of the signing run, which holds the submission id and the
`status: Accepted` line, as `evidence/signing/notarization.txt`; to fetch it again
use `xcrun notarytool info <submission-id> --keychain-profile <profile>` with the
profile the signing run used. The widget
status file path is the `CASCADE_WIDGET_STATUS_PATH` the installer records in
the LaunchAgent.

## 4. The PAR rows

The reference for every `PAR-*` row is the owner's widget v1 parity spec,
section 1 of the widget v1 parity intake (2026-09-28): the column rules (§1.1),
the provider rows (§1.2), freshness and state (§1.3).

**`PAR-A`** covers the subscription account rows. The owner reads each row and
confirms it shows live 5H and Wk values and the weekly reset, and shows needs
re-auth only for a proven-dead credential. It must be `passed`. If the widget
cannot show these, route the defect to the widget and top-level epics; never
journal `PAR-A` as deferred.

**`PAR-CO`, `PAR-GP`, `PAR-OC`, `PAR-ZA`** are the Codex, Antigravity, OpenCode
Go and Z.ai usage rows. The v1 layer keeps serving them until P2, so the P1
journal marks them `deferred-P2`. The mark is not free: the evidence file must
hold one JSON object copied from the v1 feed for that label. A bare label or a
row with a missing field is red.

Capture the row for each label from the v1 feed (`~/.cascade/accounts/quota.json`)
and save it as `evidence/par-<label>.json` with these keys:

| Key | Value |
|---|---|
| `label` | `CO`, `GP`, `OC` or `ZA`, the same label as the locator |
| `five_hour_pct` | the 5H value: a number or `null` |
| `weekly_pct` | the Wk value: a number or `null` |
| `five_hour_reset` | the 5H reset field as the feed carries it, or `null` |
| `weekly_reset` | the Wk reset field as the feed carries it, or `null` |
| `captured_at` | the feed capture time, RFC 3339 (the feed file's modification time in UTC) |

```json
{
  "label": "CO",
  "five_hour_pct": 12,
  "weekly_pct": 43,
  "five_hour_reset": "2026-10-12T14:00:00Z",
  "weekly_reset": "2026-10-16T09:00:00Z",
  "captured_at": "2026-10-12T09:00:00Z"
}
```

If the feed names a field differently, rename the key when copying; keep the
values exactly as the feed has them. A recorded failure stays `failed`.

## 5. The C6P disclosure

WidgetKit project content is not served in this release, so `C6P` is journaled
`disclosed-not-passed` and listed in the release report. Put this text in
`evidence/c6p.txt`:

> WIDGET/C6P is DISCLOSED-NOT-PASSED. The WidgetKit widgets do not show project
> content (the active project, the task summary or the five-project table) in
> this release: the status feed's `projects` list is always empty. The rows are
> deferred as DEF-P1-widget-project-rows, home P2-WID-01. The layouts (`C6`) are
> verified separately.

`C6P` is the only locator that may be `disclosed-not-passed`.

## 6. Run the verifier

The verifier reads files produced by other tools, and its seeded fixtures are
written by hand to the shape of that output. Before the owner-present session,
run the live check once over a dry-run journal built from a real capture of each
file (install output, widget status file, `codesign`, `spctl`, `notarytool`), so a
shape mismatch shows up there and not during the session. The v1 feed row
cannot be proven to come from the v1 feed; the verifier checks its shape and
values only.

The seeded tests run in the default lane and prove the verifier is red on each
defect and green on a complete journal:

```
go test -count=1 ./internal/acceptance/ -run '^TestWidgetParityJournal'
```

The live check runs the same verifier over the real journal. It fails, never
skips, when any of the three values is unset or the artifact path does not
exist:

```
WIDGET_PARITY_JOURNAL=<journal.md> WIDGET_PARITY_RC_TAG=<rc tag> \
WIDGET_PARITY_ARTIFACT=<installed .app or .dmg> \
go test -tags widgetparity -run '^TestWidgetParityJournal_Live$' ./internal/acceptance/
```

A skipped or timed-out leg leaves the release condition red.
