# internal/syncmerge fixture provenance

Spike ticket: P1-E13-W3-S27-T5. All files under `fixtures/` are hand-authored
adversarial scenarios in the B-S03.T3 per-domain JSON export envelope
(`domain`, `case`, `description`, `sideA`, `sideB`, and `expected` for the
phase-git domain only).

## Why config, memory and blob fixtures carry no `expected` field

The same implementer wrote both the merge functions and these fixtures. A
fixture carrying a hand-authored "expected" value would only confirm a wrong
merge strategy against its own author's wrong expectation -- it proves
nothing. Instead, `properties_test.go` asserts the algebraic properties
(commutativity, idempotence, associativity, tombstone dominance, no-loss)
directly against `configMergeLWW`, `memoryMergeAppend` and `blobMergeUnion`,
over every fixture in this corpus and over a deterministic property-based
generator run (seeded `math/rand`, no wall-clock entropy). A property that
holds for every fixture AND for hundreds of generated adversarial inputs is
evidence a self-authored expected value never was.

## Phase-git domain: real git as the independent counterpart

The phase-git domain is the one exception: its real counterpart is the `git`
binary itself, which is independent of this spike's author (Art.2). The
three `phase-git-*.json` fixtures document each scenario's setup and
expected outcome as a contract, but `TestPhaseMergeGit` builds the actual
two-branch git repositories fresh, in `t.TempDir()`, on every test run,
rather than replaying stored git objects -- commit hashes are
timestamp-dependent, so a byte-for-byte stored repository would not
reproduce deterministically across machines or dates. The `expected` field
in each phase-git fixture is the scenario's outcome contract (fast-forward
or `ErrPhaseStateDiverged`), verified against the real `git` binary's actual
behaviour on the repos `TestPhaseMergeGit` constructs.

- git version used to develop and verify this spike: `git version 2.51.0`
- date captured: 2026-09-07

## Blob domain: real BLAKE3 over real payloads

`blob-union-*.json` and `blob-staging-digest-mismatch.json` carry base64
payload bytes (`dataBase64`, or `fullDataBase64`/`truncatedDataBase64` for
the digest-mismatch case). The test derives each blob's declared content
address from the REAL BLAKE3 digest of its own payload
(`github.com/zeebo/blake3`, already a direct module dependency), then stages
it through `stageBlob`, so admission is exercised against real content
rather than a hand-authored hex string standing in for one.
