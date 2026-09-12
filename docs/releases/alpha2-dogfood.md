# Wave-2 Dogfood Session — v2.0.0-alpha.2

Ticket: P1-E09-W2-S18-T7. Real work session using the `v2.0.0-alpha.2`
local-snapshot binary (`darwin/arm64`, extracted from
`dist/cascade_2.0.0-alpha.2-SNAPSHOT-79bc5cc_darwin_arm64.tar.gz`) against
the actual `cascade` repository at `<repo-root>`
(this repo counts as a real codebase, per the ticket). Session run
2026-09-12, on the dev machine, with a fresh temporary `$HOME` for the
binary's own state (not the real `~/.cascade`), so the results reflect a
first-run experience rather than pre-existing local state.

## Invocations

All timestamps UTC.

**14:02:59** — `cascade doctor` (bootstrap; fresh HOME)
```
OK     backup                         rclone rclone v1.75.1
OK     doctor_selfcheck               doctor framework operational: registry, runner, and this check all ran
OK     nodes                          nodes: no nodes enrolled
OK     provider_health                provider_health: no providers registered
OK     retrieval_fusion_default       retrieval.fusion.enabled is on
OK     retrieval_index                no retrieval index has been built yet
OK     secrets/keychain-reachable     custody backend "macos-keychain" answered with 0 entries
OK     secrets/keys-resolvable        configuration references no vault keys
OK     secrets/oauth-not-expired      0 stored OAuth grant(s), none expiring within 24h
OK     secrets/patterns-loaded        7 detector pattern(s) loaded
OK     secrets/quarantine-depth       0 quarantined detection(s), threshold 25
```
Exit 0. Observation: correctly detected the real macOS Keychain backend on
the dev machine (`macos-keychain`, not the Linux `file-vault` fallback seen
in the container runs), and correctly found the real `rclone v1.75.1`
installed on this machine for the backup check.

**~14:02** — `cascade context show` (this repo, real working tree)
```
== ASI (75 tokens) ==
# Cascade Instructions — GCI
...
== ASI: Purpose (14 tokens) ==
...
== ASI: Rules (11 tokens) ==
...
== PPI (75 tokens) ==
# Cascade Instructions — GCI
...
```
Observation: correctly discovered ASI at `<sites-root>/.cascade/CASCADE.md`
and PPI at `<project-root>/.cascade/CASCADE.md` — both real,
pre-existing files on this machine from earlier scaffold work, not
generated for this test. PRI and GCI were correctly absent (no
`.cascade/CASCADE.md` at the `cascade` repo root, and a fresh HOME has no
global tier file). Deviation noted: both the ASI and PPI files are
byte-identical and their content's own heading says "tier `gci`"
regardless of which tier slot loaded them. This looks like an unfilled
scaffold template rather than a discovery bug (discovery correctly read
two distinct files at two distinct, correctly-computed directories); it is
a pre-existing authoring gap on this machine's `.cascade/` tree, not
something this gate created, and is not filed as a fresh defect since its
provenance predates this session and was not independently confirmed
against the scaffolding tool that produced it.

**~14:02** — `cascade context slice` (same directory)
```
SLOT       TOKENS  SOURCES
tier       175     4
retrieval  0       0
memory     0       0
total      175     4
```
Exit 0. Correct budget accounting: 175 tokens across the 4 tier sections
shown by `context show` (ASI body/Purpose/Rules + PPI).

**14:03:21** — `cascade daemon run` (backgrounded), then `cascade status`
```
FIELD        VALUE
version      2.0.0-alpha.2-SNAPSHOT-79bc5cc
pid          3264
uptime_s     3.043
connections  1
socket_path  /tmp/cascade-dogfood-home.8AWA2Y/.cascade/daemon.sock
health       degraded
```
Observation: daemon started and answered `status` correctly (real pid,
real uptime, correct socket path under the temp HOME). `health: degraded`
observed again here — same as the Linux and macOS-approximation runs,
tracks back to the `retrieval_index`/`conductor.executor` doctor finding
recorded in `docs/waves/w2-gate-report.md` §3. The daemon startup also
printed the same misleading "daemon not running; embedded mode" line seen
on Linux (DEFECT-daemon-run-prints-daemonless-warning.md).

**14:03:21** — `cascade recall "minisign"` (real query, real repo content
mentions minisign extensively: `.goreleaser.yaml`, `docs/developer/release.md`)
```
error: invalid-input: invalid-input: recall.query: invalid-input: corpus: membership has an invalid scope reference
exit: 2
```
Observation: this is the **third** independent reproduction of the same
recall failure in this gate (Linux container, macOS clean-HOME simulation,
and now this real dogfood session against real repository content with a
query term that genuinely appears throughout the codebase). Filed as
DEFECT-recall-broken-fresh-install.md, P1. The dogfood session could not
complete its minimum-required `recall` exercise successfully — this is
recorded honestly rather than papered over.

## Session assessment

Context slicing and doctor/status/daemon surfaces behaved correctly and
usefully for a real work session against a real repository. Recall did
not work at all in this session, which is a genuine, disqualifying defect
for that surface's dogfood requirement — the acceptance criterion "dogfood
session recorded... covered at minimum cascade context slice and cascade
recall" is met in the sense that both commands were run and their real
behavior recorded, but `cascade recall` itself did not function correctly.
This gap is not filed as a separate defect from
DEFECT-recall-broken-fresh-install.md — it is the same defect, observed a
third time.
