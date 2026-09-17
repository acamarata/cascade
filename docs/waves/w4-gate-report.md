# Wave-4 Hardening Gate Report — v2.0.0-alpha.4

Ticket: P1-E19-W4-S42-T7. Quality Constitution Art.11 wave gate for Wave 4.

Date: 2026-09-17. Artifact: `v2.0.0-alpha.4-SNAPSHOT-346c928` (the closing
run; earlier sections were captured against `…-a6f86c6`, `…-7e329cb` and
`…-f001a70` as each finding was fixed and the pipeline re-run), built from
`p1-integration` through the real release pipeline. The local annotated tag
`v2.0.0-alpha.4` exists at that head and was **never pushed** (R-16.23,
Art.11.1): `git ls-remote --tags origin 'v2.0.0-alpha.*'` returns nothing.

**Verdict: PASSED, after the gate found and this wave fixed nine defects
the suite was blind to.** Art.11.2's W4 condition — install → `init` →
`doctor` green on clean macOS **and** clean Linux, from pipeline artifacts,
with brew and OCI verified — is met on both platforms. Nothing below is
smoothed over.

## 1. Precondition check

All five W4 epic acceptance tickets were Article-3 done before this gate
opened:

| Ticket | Epic | State |
|---|---|---|
| `P1-E15-W4-S33-T4` | O — plugin SDK example | done |
| `P1-E16-W4-S35-T5` | P — `cascade init` acceptance | done |
| `P1-E17-W4-S38-T5` | Q — laptop⇄server sync | done, `gate_only` (R-14.276) |
| `P1-E18-W4-S40-T5` | R — supervision/telemetry | done |
| `P1-E19-W4-S42-T5` | S — lose-the-laptop drill | done, `gate_only` |

## 2. Release pipeline — VERIFIED

`goreleaser release --snapshot --clean`, with a throwaway minisign keypair
under `.minisign-e2e/` (gitignored, never the real key).

| Artifact | Result |
|---|---|
| Archives | 5 (darwin amd64/arm64, linux amd64/arm64, windows amd64) |
| `checksums.txt` | **10/10 OK** under `shasum -a 256 -c` |
| minisign signature | **verified**, trusted comment `cascade 2.0.0-alpha.4-SNAPSHOT-346c928 checksums` |
| SBOMs | 5, one per archive (syft) |
| Homebrew cask | `dist/homebrew/Casks/cascade.rb`, 4 sha256 entries, **all four match built archives** |
| OCI images | 2 (`…-amd64`, `…-arm64`), multi-arch manifest |

**Provenance attestation is CI-side only**, unchanged from W2 and W3: it
needs a GitHub OIDC token, which exists in `.github/workflows/release.yml`
and not on a developer's machine. The ticket's AC lists it among the
pipeline artifacts; it is produced by the pipeline, just not by the local
half of it. Recorded rather than claimed.

**No publish occurred.** `--snapshot` implies `--skip=publish`; the cask
was copied to a throwaway local tap for `brew style`/`audit` and that tap
is removed at the end of this gate. Nothing reached a registry, a tap, or
a GitHub release.

## 3. Clean macOS — VERIFIED

Fresh `HOME`, binary extracted from `dist/…_darwin_arm64.tar.gz`. Nothing
from the repo tree.

| Check | Result |
|---|---|
| `cascade version` | `2.0.0-alpha.4-SNAPSHOT-346c928` |
| `init --check --yes --no-daemon`, virgin | **exit 3**, plan names the cascade home and the database |
| `init --yes --no-daemon` | **exit 0**, no "would create" lines on a run that is creating |
| `cascade doctor` | **exit 0** |
| `init --check --yes --no-daemon`, converged | **exit 0** |
| `~/.cascade` afterwards | `data/` only — no stray database at the root |

## 4. Clean Linux — VERIFIED

`docker run --platform linux/arm64 debian:stable-slim`, no Go toolchain,
`dist/…_linux_arm64.tar.gz` extracted inside.

| Check | Result |
|---|---|
| `init --check --yes --no-daemon`, virgin | **exit 3**, same two plan lines as macOS |
| `init --yes --no-daemon` | **exit 0**, storage `/root/.cascade/data/cascade.db` |
| plugin catalog | five builtins, each rendered `[built in]` |
| `cascade doctor` | **exit 0**, every check OK |
| `init --check`, converged | **exit 0** |
| `cascade plugin list` | five rows, `SOURCE = builtin` |

The two platforms produce the same transcript, line for line, modulo
paths.

## 5. brew and OCI — VERIFIED

**brew.** `brew style --cask` → *1 file inspected, no offenses detected*.
`brew audit --cask --skip-style` → **exit 0**. Both against the
pipeline-built cask, installed into a throwaway tap
(`cascade-gate/homebrew-w4gate`) because `brew audit` on a bare path is
disabled in current Homebrew. All four sha256 entries match the built
archives. The cask's `desc` needed fixing to pass style — see finding 5.

**OCI.** `docker run` the pipeline-built image:

| Check | Result |
|---|---|
| `cascade version` in the container | `2.0.0-alpha.4-SNAPSHOT-346c928` |
| image user | `65532` (distroless non-root; the image has no shell and no `id`) |
| `cascade daemon run` as pid 1 | socket at `/home/nonroot/.cascade/daemon.sock` |
| `cascade status` | **`health: ok`**, six subsystems running, one honestly `skipped` |
| `cascade doctor` | **exit 0** |

## 6. The defects this gate found

Nine. Every one was found by running the shipped binary; not one was
reachable from the tree's own test suite as it stood.

### 6.1 `init --check` could never exit 0 — FIXED (`25f905e`, R-14.280)

Step 9 planned `run cascade doctor --first-run` unconditionally. A health
check changes nothing, so `--check`'s plan was never empty and it exited 3
on every machine including a converged one — the flag's one
differentiating behaviour, and it never happened. Every `--check` test ran
against a virgin home, where the exit code is 3 for half a dozen real
reasons, and this one hid behind them.

### 6.2 …and then `--check` exited 0 about a virgin Windows machine — FIXED (`f001a70`, R-14.280)

Uncovered by fixing 6.1. On Windows `DaemonSupported` is false, so step 8
skips outright and its two plan entries — the ones carrying every other
platform's virgin run — do not exist. With the doctor entry gone, a
machine with **no cascade home at all** planned nothing and exited 0.

The real cause is older than either change: the plan never included the
machine's own existence. Creating `~/.cascade` and creating the database
are the first and largest changes a run makes, and both were merely said.
Every platform's virgin `--check` had been answering 3 by accident.

The new gate takes the platform as a parameter. Every other `--check` test
ran on the fixture's darwin; the wizard takes GOOS as an injected field
precisely so a test can vary it, and none was.

### 6.3 Thirty CLI results printed Go's default formatting — FIXED (`25f905e`, R-14.253)

`cascade backup target list` printed `map[targets:[]]`; `cascade backup
list` printed `{[]}` — on the epic whose acceptance drill is recovering a
lost laptop. `output.Writer.Result` calls `fmt.Stringer` and nothing else.
A type-aware gate now walks every `*output.Writer.Result(x)` call with
`go/packages` and requires `types.Implements(typeOf(x), fmt.Stringer)`. It
found thirty offenders; all thirty have String methods now. A ruling is
not a gate.

### 6.4 A sqlite handle leaked per sync verb — FIXED (`b693f26`)

Found by the Windows CI lane, which cannot delete an open file. Every
`sync` verb opened the store and never closed it.

### 6.5 The plugin surface was blind to the plugins this binary ships — FIXED (`a6f86c6`)

`cascade plugin list` showed nothing on a fresh install; `plugin info
pbd` said it was not installed while `cascade pbd status` worked.
Builtins are compiled in and have no store record. `list` now carries a
SOURCE column, `info` answers from the registry, and
`enable|disable|remove` refuse a builtin with that fact.

### 6.6 The brew cask failed `brew style` — FIXED (`.goreleaser.yaml`)

`desc` may not begin with the cask's own name nor end with a full stop.
It did both.

### 6.7 init asked a question whose answer nothing read — FIXED (`e28693a`, R-14.277)

`Enable <plugin>?` per catalog entry, answer discarded: every entry is a
builtin, mounted unconditionally, with no enabled flag anywhere.
Answering no changed nothing except what the operator believed. The
reconverge half was the same defect in a second place — the plugin
convergence was computed, printed as `would enable plugin X`, and never
passed to `applyConvergence`, so `--enable-plugin` printed a plan and
performed none of it.

### 6.8 The retrieval marker was computed twice, and the copies disagreed — FIXED (`8334d97`, R-14.278)

`cascade recall index verify` reported the marker current while `cascade
doctor` reported it drifted and exited 5. Two implementations: the
daemon's trimmed `git status --porcelain`, the doctor's own copy hashed
the raw bytes. Every working tree with an uncommitted change — every
developer machine, permanently — produced two different digests.

The copy carried a comment asserting the algorithm was identical to the
daemon's. It was true when written and checked by nobody, which is
exactly what stops a reader from comparing the two. Both halves hash an
EMPTY status identically, and a clean checkout is every fixture and every
CI run, so the suite could not see it.

### 6.9 init advertised a database nothing opened — FIXED (`67f30a2`, R-14.279)

`init` reported `~/.cascade/cascade.db` as this machine's storage and its
probe created an empty database there. Everything else opens
`~/.cascade/data/cascade.db`. The summary card is the one artifact of the
wizard an operator keeps; every reasonable move from that line acted on an
empty file while the real one sat one directory down.

## 7. Surfaces exercised

Against the shipped artifact on macOS, real `HOME`, no fixtures.

| Surface | Result |
|---|---|
| `plugin list` / `info` | five builtins, `SOURCE = builtin`; `info pbd` says "built into this binary: always present, always active, and not removable" |
| `plugin info <unknown>` | not-found, naming both populations |
| `plugin enable/disable/remove <builtin>` | refused, `unsupported`, each message explaining that a builtin is always active |
| `node list` | "no enrolled nodes" |
| `sync status` | seven domains, each with strategy, eligibility and cursor |
| `sync conflicts list` | "no conflicts journaled" |
| `sync run` (daemonless) | refused: "no run path is wired, so nothing would be synced; reporting success here would be a lie an operator could not see through" |
| `backup target add` / `list` | target stored and rendered as a table |
| `backup create` | refused: needs `--yes` under `CASCADE_NO_INPUT=1` |
| `backup verify` | refused: `CASCADE_BACKUP_AGE_IDENTITY` not set; run the recovery-key ceremony first |
| `recall index rebuild` → `recall <term>` | rebuild reports its marker; an unbuilt index refuses with "no retrieval index has been built yet" rather than an empty answer |

**Residuals, stated rather than glossed:**

- **Node enrolment needs a second machine.** `node enroll`, heartbeat,
  one-shot dispatch and the node journal cannot be exercised from one
  host. This is the same hardware residual `Q/S-38.T5` and `S/S-42.T5`
  carry as `gate_only`, and it is already one of the §7 prerequisites
  tracked against the release gate. Local node verbs are exercised above.
- **`backup create` and `backup key export` are owner-executed.** They
  require an interactive local-presence ceremony and a recovery identity
  by design; refusing under `CASCADE_NO_INPUT=1` is the correct
  behaviour, and it is what was verified.

## 8. W-3 findings, re-checked on this artifact

The W-3 gate closed with one P1 and five P2s seeded to W-4 triage.

| W-3 finding | Severity | State on alpha.4 |
|---|---|---|
| `cascade fleet sessions`: method not found | P1 | **FIXED** — returns rows |
| `recall index rebuild` prints a raw Go struct | P2 | **FIXED** by 6.3's sweep |
| `recall` cannot distinguish "no match" from "empty index" | P2 | **FIXED** — refuses with "no retrieval index has been built yet" |
| `execute.go` reports `ErrNoLane` for every `Select` error | P2 | not re-checked here; needs a provider credential |
| `provider list` health empty after a live verify | P2 | not re-checked here; needs a provider credential |
| R5 reserves "status" by bare name across namespaces | P2 | open, carried to W-5 |

The two not re-checked need a real provider credential, which this gate
has no authorization to obtain. Said plainly rather than marked verified.

## 9. Defect triage (Art.9)

| ID | Severity | Status |
|---|---|---|
| `init --check` could never exit 0 | P1 | FIXED this wave |
| `--check` exited 0 on a virgin Windows machine | P1 | FIXED this wave |
| 30 CLI results printing Go struct dumps | P1 | FIXED this wave |
| sqlite handle leaked per sync verb | P1 | FIXED this wave |
| plugin surface blind to builtins | P1 | FIXED this wave |
| brew cask fails `brew style` | P1 | FIXED this wave |
| init asks a question nothing reads | P1 | FIXED this wave (`P1-E16-W4-S35-T14`) |
| retrieval marker computed twice, copies disagree | P1 | FIXED this wave (`P1-E19-W4-S42-T8`) |
| init advertises a database nothing opens | P1 | FIXED this wave (`P1-E16-W4-S35-T15`) |
| a writing run said "would create" | P2 | FIXED this wave (same commit as 6.2's guard) |
| 18 independent constructions of the database path | P2 | OPEN — W-5 (`T0-OPEN-FOLLOWUPS.md` §9) |
| `backup target add`'s required flags are not visible in its usage line | P2 | OPEN — W-5 |
| R5 reserves "status" by bare name across namespaces | P2 | OPEN — W-5, carried from W-3 |

**No P0 or P1 defect remains open against Wave 4.** Every P1 above was
found by this gate, fixed in this wave, and re-verified from a rebuilt
artifact — sections 3, 4 and 5 are the closing run, not the first one.

## 10. What the gate says about the tests

Worth recording, because it is the fourth wave in a row it has been true:
**every significant defect this gate found was invisible to the suite**,
and each was invisible for a reason worth naming.

- A fixture in a state nobody is ever in. A clean checkout hashes
  identically under both marker implementations (6.8); a virgin home exits
  3 for reasons that mask a false plan entry (6.1).
- A test that agrees with the bug. Two init tests asserted the plugin
  selection that never happened (6.7); one was accidentally true because
  the fixture's single catalog entry shared a name with the setup file's.
- A premise the fixture did not actually meet. The "converged machine"
  test left the database file out, so it described a half-set-up machine
  while asserting an empty plan about it (6.2).
- A platform nothing varied. Every `--check` test ran on the fixture's
  darwin, and the defect lived where `DaemonSupported` returns false (6.2).
- A comment instead of a check. "Identical algorithm to …" was true when
  written, verified by nobody, and is precisely what stops a reader
  comparing the two (6.8).

Each fix landed with a gate that fails on the mutation, and each mutation
was run.

## 11. Sign-off

Art.11.1 (tagged, signed, local-only, never pushed) — met. Art.11.2 (the
W4 scoped condition: the full `init` path, install → init → doctor green
on macOS **and** Linux from pipeline artifacts, brew and OCI verified) —
met on both platforms, sections 3–5. Art.11.3 (real use, evidence
recorded) — met: the dogfood run is what found all nine findings.

No P0 or P1 defect remains open against Wave 4. The three open items in §9
are P2s, seeded to W-5 triage per Art.9.

**Wave 4 is CLOSED.** T0 countersigns this report as an agent claim
(Art.1.5, register §B.11).
