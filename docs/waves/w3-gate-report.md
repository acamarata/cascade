# Wave-3 Hardening Gate Report — v2.0.0-alpha.3

Ticket: P1-E14-W3-S30-T5. Quality Constitution Art.11 wave gate for Wave 3.

Date: 2026-09-16. Artifact: `v2.0.0-alpha.3-SNAPSHOT-0aa044c`, built from
`p1-integration` at `0aa044c` through the real release pipeline. The local
annotated tag `v2.0.0-alpha.3` exists at that head and was **never pushed**
(R-16.23, Art.11.1): `git ls-remote --tags origin` returns nothing.

**Verdict: the gate did its job, and Wave 3 does not close yet.** It found
five defects that every unit test in the tree was blind to, four of them
fixed in this wave, and one P1 that blocks close. Details below; nothing
here is smoothed over.

## 1. Release pipeline — VERIFIED

`goreleaser release --snapshot --clean` under the machine-wide `flock`,
with a throwaway minisign keypair under `.minisign-e2e/` (gitignored,
verified with `git check-ignore`). Release succeeded.

`dist/` carries the five archives (darwin/linux × amd64/arm64, windows
amd64), `*_checksums.txt` + `.minisig`, one real `syft` SBOM per archive,
the homebrew cask definition, and two local ghcr images — none pushed.

- `minisign -Vm … -p .minisign-e2e/verify-e2e.pub` → "Signature and comment
  signature verified", exit 0.
- `shasum -a 256 -c …_checksums.txt` → **10/10 OK**.

Provenance attestation is absent and cannot be produced locally: cosign
keyless signing needs a GitHub OIDC token, so it runs CI-side only. This is
`.goreleaser.yaml`'s own documented design, unchanged since the W2 gate
recorded the same thing.

## 2. The five defects this gate found

Every one of these is the same class: **a production path whose only
callers were tests.** No unit test could see any of them, because each test
supplied the wiring the product was missing.

### 2.1 `cascade run` was never mounted — FIXED (`01c72ff`/`29cdab2`)

The K epic's only user-facing door did not exist in the binary.
`mountRunCmd` carried a comment saying the one-line follow-up was deferred
because `root.go` was owned by concurrent work; it was never done. Every
test of `run` mounted the command itself, so all of them stayed green.

Also repaired: `TestRunCmd_OnlyModelDoor` had quietly stopped asserting —
it added its own `run` command and skipped it by pointer identity, so the
moment a real one was mounted beside it the walk flagged the real one.

### 2.2 The whole `cascade pbd` namespace was absent — FIXED (same commit)

Nothing in `cmd/` or `internal/` imported `plugins/pbd`, so its `init()`
never ran, its manifest never reached `plugin.Builtins()`, and the N epic —
the dogfood proof of the O-epic plugin host — shipped in no binary. Same
class as `DEFECT-builtin-plugin-registry-unreachable.md` records for codex
and opencode, caught this time in the artifact.

A namespace whose manifest fails to load now mounts anyway and refuses with
the reason. A plugin that silently vanishes from `--help` is precisely how
this survived a wave.

Note: `cascade pbd status` is **`cascade pbd summary`** in the artifact.
`pkg/plugin/validate.go` rule R5 reserves "status" by bare name regardless
of namespace, so the manifest cannot use it. Recorded in `status.go` when
that ticket landed; recorded here too, because the contract's literal
`pbd status` does not exist. Whether R5 should be namespace-aware is an
Art.9 P2 for W-4 triage.

### 2.3 `registry.UpsertLane` had no production caller — FIXED (`b5b3d4a`)

`cascade provider add` wrote a provider row and no LANE. The router selects
over lanes, so the lane table was empty on every machine, for every
provider, and no dispatch could ever be routed.

The lane's state is evidence, not optimism: live-verified → `available`,
`--no-verify` → `unknown`. Pooled lanes are namespaced by pool, because
`lane_name` is the primary key and two pool members would otherwise
silently overwrite each other.

### 2.4 The security pipeline was never wired — FIXED (`e94adc9`)

`Pipeline.Ready()` has gated every call since K/S-22.T1, and **nothing in
the tree implemented Classifier, TaskClassTable, PolicyEvaluator or
SensitivityGate outside test doubles.** A real `cascade run` against a real,
live-verified provider answered `conductor: security pipeline not ready` —
the product's core function, refused at its own door. R-14.243.

### 2.5 An empty quota spill order vetoed every dispatch — FIXED (`0aa044c`)

The true cause behind "no candidate lane", four layers below the message:
`defaultQuotaConfig()` returns an empty `SpillOrder`, the daemon calls
`ParseQuotaConfig(nil)`, and `NextLane` iterates that empty order — so it
returned `ErrAllLanesExhausted` for every request, for every provider, on
every machine. R-14.245: fail-closed governs authorization, not preference.

## 3. Clean Linux (real, via Docker) — VERIFIED

`docker run --platform linux/amd64 debian:stable-slim`, no Go toolchain,
`dist/*_linux_amd64.tar.gz` extracted inside. Every check ran against the
extracted binary.

### W1 conditions — all VERIFIED

| Check | Result |
|---|---|
| `cascade doctor`, first command, virgin HOME | exit 0, **11/11 OK** |
| `cascade daemon run` | socket opened at `~/.cascade/daemon.sock` |
| `cascade status` | exit 0, **`health: ok`** |
| `cascade status --json` from a second shell (IPC round trip) | exit 0 |
| `cascade config list` | exit 0 |
| `cascade doctor` **with the daemon up** | exit 0, 0 non-OK |

Two W2-gate defects are confirmed FIXED by this run: `status` reported
`health: degraded` at alpha.2 and reports `ok` here; `doctor` with the
daemon up exited **5** with `ERROR retrieval_index` at alpha.2 and exits 0
here. `conductor.executor` now reports `running (executor constructed)`
instead of failing to start.

### W2 surfaces — VERIFIED

| Check | Result |
|---|---|
| `cascade context slice` / `show` on a real git-repo fixture | exit 0, correct tier discovery and token budget |
| `cascade memory remember` / `list` | exit 0, record stored and returned |
| `cascade vault list` | exit 0, **formatted output** (the alpha.2 raw-Go-struct defect is fixed) |
| `cascade recall index rebuild` then `cascade recall <term>` | exit 0 |

`recall` no longer fails with alpha.2's "corpus: membership has an invalid
scope reference"; before an index exists it now refuses with an actionable
`no retrieval index has been built yet`. Two findings recorded rather than
fixed here:
- `cascade recall index rebuild` prints a raw Go struct `{0 0 0 true }` —
  same output-defect class as the fixed `vault list`. **P2, W-4 triage.**
- That rebuild indexed **0 sources** on a plain git repo, so `recall` then
  answers "no results" for a term that is demonstrably in the tree. An empty
  answer is currently indistinguishable from an unbuilt index, which the
  command's own help text promises it will not be. **P2, W-4 triage.**

### W3 surfaces

| Check | Result |
|---|---|
| `cascade provider add` against the REAL compat-sub endpoint | **VERIFIED**, exit 0 |
| `cascade provider list --json` | exit 0, provider present, `lanes=1` |
| `cascade fleet usage` | exit 0 |
| `cascade fleet sessions` | **FAILED**, exit 3 |
| `cascade pbd summary` / `board` / `validate` | exit 0 |
| `cascade run` end-to-end | **blocked at the credential boundary — see §4** |
| `cascade init` | not checked (Epic P is W-4; Art.11.2) |

**`cascade provider add` is a real Art.2 proof.** The shape probe selected
the `anthropic` driver, the live micro-verify returned the provider's real
model list (ten models), and the credential was persisted to the vault
under `provider.compatsub.key`. That model list can only have come from a
live HTTP 200 against the real endpoint.

Two defects recorded from this section:
- **`cascade fleet sessions` → `method not found: fleet.sessions.list`.**
  The handler is not registered in the daemon. An L-epic surface that does
  not work in the artifact. **P1, W-4 triage.**
- `cascade provider list --json` reports `"health": ""` and all-zero
  capabilities after a successful live verify, and `cascade provider health`
  then exits 5 with "1 provider(s) not healthy". The verify's own evidence
  is not recorded on the record. **P2, W-4 triage.**

## 4. The dispatch chain, and the P1 that blocks close

Tracing `cascade run` end-to-end through the tagged artifact moved the
failure forward four times, each one a real defect fixed in turn:

1. `security pipeline not ready` → §2.4
2. `no candidate lane` (no lanes existed) → §2.3
3. `no candidate lane` (empty spill order) → §2.5
4. `secrets: no live grant authorises reading provider.compatsub.key;
   issue one with 'cascade vault grant provider.compatsub.key'`

Step 4 is **correct, designed behaviour** and is R-14.243's property 8
proven in the artifact: fail-closed at the true credential boundary, per
request, naming the key and the remedy, with no prompt from a process that
has nobody to answer it.

### P1-W3-01 — elevated verbs cannot succeed from a release artifact

Issuing the grant needs the elevation gate, and **no elevated verb can be
exercised from a shipped binary on either platform today**:

- Linux release artifact: `helper not enrolled and no local authenticator
  is available`.
- macOS release artifact: `cascade elevate-helper --enroll` → `elevation:
  no hardware/OS keystore is available on this host` (release binaries are
  built `CGO_ENABLED=0`, so the platform backend is not compiled in).
- macOS local `go run` build: enrolment reaches the Keychain and fails with
  `OSStatus -34018` (errSecMissingEntitlement) — an unsigned binary.

This is **pre-existing, not introduced by this wave**: the W2 gate recorded
`cascade vault get` refusing with the identical message and accepted the
refusal as correct behaviour. What changed is that the credential path now
DEPENDS on it, so `cascade run` cannot complete on any machine until it is
fixed. Under Art.6 a shipped component whose verbs cannot work is fixed or
removed; under Art.9 this is a **P1, and Wave 3 does not close while it is
open**.

Filed as `DEFECT-elevated-verbs-unusable-from-artifact.md` with the
proposed fix (a file-backed elevation keystore fallback, following
`secrets.SelectCustody`'s existing OS-keychain→encrypted-file precedent,
recorded as the weaker proof it is and reported by `cascade doctor`).

**Nothing was simulated to get past this.** No mock provider, no fabricated
grant, no "verified" claim for a call that did not happen (Art.1, Art.2).

## 5. macOS run — honestly limited

There is no second, genuinely clean Mac. Per register §A.12 a fresh macOS
user account is the accepted stand-in; this pass used a throwaway
`CASCADE_HOME` and the extracted `darwin_arm64` artifact, which is weaker
and is labelled as such. `cascade version` runs from the artifact; the
elevated-verb path fails as §4 records.

## 6. Dogfood

`cascade pbd` was run against **cascade's own P1 planning tree** (408 real
tickets), which found a sixth defect the moment it was tried: every pbd
command failed with `unknown ticket field "security_class"`. The flag is
ratified (R-14.215 item 5) and the schema did not declare it. Nothing caught
it because the only test that reads the real tree,
`TestDogfoodConvertRealPlan`, is skipped unless `CASCADE_PBD_DOGFOOD_SRC` is
set, and it never was.

Fixed (`e4fff00`/`16cc96e`). With the env var set, the conversion now reads
all 408 tickets and its idempotent re-run reports no changes. That run also
surfaced two genuine contract defects in E-AA/S-55/T-8 (missing Article-3
DoD clauses), repaired in the planning tree.

## 7. Defect triage (Art.9)

| ID | Severity | Status |
|---|---|---|
| `cascade run` unmounted | P1 | FIXED this wave |
| `cascade pbd` namespace absent | P1 | FIXED this wave |
| No router lane for a registered provider | P1 | FIXED this wave |
| Security pipeline unwired | P1 | FIXED this wave |
| Empty spill order vetoes every dispatch | P1 | FIXED this wave |
| PEWS schema rejects `security_class` | P1 | FIXED this wave |
| **Elevated verbs unusable from an artifact** | **P1** | **OPEN — blocks W-3 close** |
| `cascade fleet sessions`: method not found | P1 | OPEN — W-4 |
| `execute.go` reports ErrNoLane for every Select error | P2 | OPEN — W-4 |
| `provider list` health empty after a live verify | P2 | OPEN — W-4 |
| `recall index rebuild` prints a raw Go struct | P2 | OPEN — W-4 |
| `recall` cannot distinguish "no match" from "empty index" | P2 | OPEN — W-4 |
| R5 reserves "status" by bare name across namespaces | P2 | OPEN — W-4 |

## 8. Sign-off

Art.11.1 (tagged, signed, local-only) — met. Art.11.2 (cumulative W1+W2+W3
surface set, nothing out of scope asserted) — met, with `cascade run`'s
final leg blocked by P1-W3-01. Art.11.3 (real use, evidence recorded) —
met: the dogfood run is what found the schema defect.

Wave 3 is **held open** on P1-W3-01, per the ticket's own rule that no P0/P1
blocks may be open at close. T0 countersigns this report as an agent claim
(Art.1.5, register §B.11).
