# Widget parity journal (template)

Copy this file next to the evidence directory it names, fill every row, and give
the copy to the verifier (`docs/widget/parity-checklist.md` has the steps).
Rows are `| WIDGET/<locator> | <status> | <evidence path> | <rc tag> | <artifact sha256> |`.

- Status is `passed`, `failed`, `disclosed-not-passed` or `deferred-P2`. `pending` is a
  placeholder and is red.
- `disclosed-not-passed` is valid only for C6P, and only when its evidence cites
  DEF-P1-widget-project-rows.
- `deferred-P2` is valid only for PAR-CO, PAR-GP, PAR-OC and PAR-ZA, and only when the
  evidence file is one JSON object copied from the v1 feed for that label (see the PAR
  section of the runbook). PAR-A must be `passed`. A recorded `failed` is never
  downgraded to `deferred-P2`.
- Evidence paths are relative to this file's directory. SIGNING names a directory holding
  `codesign.txt`, `spctl.txt` and `notarization.txt`. FRESH-INSTALL names a directory
  holding `install.json` (the saved `cascade widget install --json` output) and
  `widget-status.json`.
- The rc tag is the release candidate under test. The artifact sha256 is the sha256 of
  the installed `.dmg`, or of `Contents/MacOS/CascadeMenuBar` for an installed `.app`.
  The verifier recomputes it; a typed value only has to agree.

## What each locator verifies

Parity reference for the PAR rows: `ref/widget-v1-parity/INTAKE-2026-09-28.md` §1 (the
owner's widget v1 parity spec). The sentence under each PAR locator is its §1 rule.

- WIDGET/C1: the popover and the desktop fleet window each show exactly one row per enrolled provider profile or pool, matching the registry count.
- WIDGET/C2: every row shows both windows, each as a percentage or an em-dash and as a countdown or an em-dash, never a made-up number.
- WIDGET/C3: the "updated N ago" label follows the snapshot's generated_at and keeps ageing while status reads fail.
- WIDGET/C4: a provider forced into auth-required by a real revoked credential renders amber; clicking it opens Terminal running the re-auth verb for that provider, and for a key provider the confirmation shows the remedy line.
- WIDGET/C4B: a primary credential entry that is expired but holds a refresh token reads ok again within one usage poll and never shows re-auth; amber appears only after the refresh token is revoked.
- WIDGET/C5: a provider with no quota source shows em-dashes, never a zero or a guess.
- WIDGET/C6: the WidgetKit small, medium and large widgets render three distinct layouts on a 30 second timeline.
- WIDGET/C6P: the WidgetKit project content (active project, task summary, project table) is not served in P1; recorded as DISCLOSED-NOT-PASSED with the deferral DEF-P1-widget-project-rows (home P2-WID-01) and listed in the release report.
- WIDGET/C7: `cascade widget install`, `status`, `uninstall` and a second `uninstall` converge against the real launchd agent.
- WIDGET/C8: a provider-state change reaches the popover within 30 seconds, by SSE or by the poll fallback.
- WIDGET/C10: with "Click wallpaper to reveal desktop" set to Always, a click on Refresh fires and a header drag moves the panel.
- WIDGET/C11: with the running bundle moved to the Trash, the bundle-path guard fires within one poll and `cascade doctor --widget` reports RUNNING_FROM_TRASH.
- WIDGET/SB1: with the SSE connection killed, a poll delivers the next change within 30 seconds.
- WIDGET/SB2: a real pushed attention item shows as a badge in the popover header within one daemon tick.
- WIDGET/SB3: an enrolled node's presence state is visible in the popover or the desktop window.
- WIDGET/SB4: the active jobs count is live from the jobs domain; an em-dash appears only when the daemon has no jobs domain.
- WIDGET/FRESH-INSTALL: first_rows_at in widget-status.json minus installed_at in the saved install output is 30 seconds or less.
- WIDGET/SIGNING: `codesign -dv --verbose=4`, `spctl -a -vv` (source Notarized Developer ID) and the notarytool submission id are recorded for the installed artifact, and the CDHash matches the artifact.
- WIDGET/PAR-A: (intake §1.2, Claude rows) each subscription account row shows live 5H and Wk values and the weekly reset, and shows needs re-auth only for a proven-dead credential (§1.3).
- WIDGET/PAR-CO: (intake §1.1 and §1.2, Codex row) windows are assigned by length, 6 hours or less to 5H and longer to Wk; v1 serves this row until P2, so it is deferred-P2 with a captured v1 feed row.
- WIDGET/PAR-GP: (intake §1.2, Antigravity row) the worst remaining fraction over every quota'd model fills 5H and Wk stays an em-dash; v1 serves this row until P2, so it is deferred-P2 with a captured v1 feed row.
- WIDGET/PAR-OC: (intake §1.2, OpenCode Go row) the dashboard windows, or the local ledger when the dashboard session is dead, fill 5H and Wk; v1 serves this row until P2, so it is deferred-P2 with a captured v1 feed row.
- WIDGET/PAR-ZA: (intake §1.2, Z.ai row) the quota-limit document maps the 5 hour token window to 5H and the weekly window to Wk; v1 serves this row until P2, so it is deferred-P2 with a captured v1 feed row.

## Journal

| Locator | Status | Evidence | RC tag | Artifact sha256 |
|---|---|---|---|---|
| WIDGET/C1 | pending | evidence/c1.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C2 | pending | evidence/c2.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C3 | pending | evidence/c3.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C4 | pending | evidence/c4.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C4B | pending | evidence/c4b.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C5 | pending | evidence/c5.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C6 | pending | evidence/c6.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C6P | disclosed-not-passed | evidence/c6p.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C7 | pending | evidence/c7.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C8 | pending | evidence/c8.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C10 | pending | evidence/c10.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/C11 | pending | evidence/c11.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/SB1 | pending | evidence/sb1.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/SB2 | pending | evidence/sb2.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/SB3 | pending | evidence/sb3.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/SB4 | pending | evidence/sb4.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/FRESH-INSTALL | pending | evidence/fresh-install | `<rc-tag>` | `<sha256>` |
| WIDGET/SIGNING | pending | evidence/signing | `<rc-tag>` | `<sha256>` |
| WIDGET/PAR-A | pending | evidence/par-a.txt | `<rc-tag>` | `<sha256>` |
| WIDGET/PAR-CO | deferred-P2 | evidence/par-co.json | `<rc-tag>` | `<sha256>` |
| WIDGET/PAR-GP | deferred-P2 | evidence/par-gp.json | `<rc-tag>` | `<sha256>` |
| WIDGET/PAR-OC | deferred-P2 | evidence/par-oc.json | `<rc-tag>` | `<sha256>` |
| WIDGET/PAR-ZA | deferred-P2 | evidence/par-za.json | `<rc-tag>` | `<sha256>` |
