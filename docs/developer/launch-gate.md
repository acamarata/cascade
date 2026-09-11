# The launch gate: the owner's pre-publish checklist

Status: active from Wave 6 (P1-E27-W10-S56-T3). This is the owner's
pre-publish gate list (06-FORGE-SPEC.md §7: any publish — npm/brew/
releases/OCI — is a GATED action, owner-authorized, never automatic). It
does not run itself: the owner reads this file and runs each command
below by hand, before authorizing either of the two points in the release
chain that consume it.

## The two gate runs

1. **Pre-rc-cut**, before AA/S-56.T4 cuts a `v2.0.0-rc.N` tag. T4's own
   acceptance criteria name this checklist in its pre-gate doc set: the
   tag is cut only when every AA/S-55 ticket AND every pre-gate AA/S-56
   doc ticket (T1 CHANGELOG, T2 security posture, **T3 this checklist**,
   T5 API stability) is done.
2. **Pre-promotion**, before AA/S-56.T6 promotes an rc to the `v2.0.0`
   release. AB/S-58.T7's release gate verifies Art.8's evidence set
   against the cut rc separately — that is the post-rc verification; this
   checklist is the pre-publish list that precedes it, and the two never
   merge into one gate.

Run every command below at the repo root, on a clean tree, at each of the
two points above.

## Automated gates

### (a) License report — A-T3

The dependency-license allowlist gate, implemented in
`internal/build/licenses.go` so it runs locally as well as in CI.
Allowlist: MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC, CC0-1.0
(dependency-rules.md #2). Fails on any GPL/LGPL/AGPL/SSPL dependency or
any module missing from the maintained registry (fail-closed, unknown is
a violation, not a pass).

```
go test ./internal/build/ -run TestLicenses_RealTreeGreen
```

**Red condition:** non-zero exit, or a reported violation naming a
copyleft or unregistered module.

CI runs the same gate plus the independent `go-licenses` tool as a
second classifier in the `supply-chain.yml` workflow's `licenses` job —
see `docs/developer/ci.md`'s lane map. This checklist re-runs the local
half so the owner reads a result from their own machine, not only CI's.

### (b) Secret scan — this ticket

`internal/build/secretscan.go` scans every git-tracked, non-test file for
credential-shaped content, using the shared `internal/secrets` detector
(the same pattern/entropy engine the redaction/quarantine path uses) at
its pattern-only signals — the entropy signal is deliberately disabled
for this gate (see the file's package doc for why: run at full strength
against this repo's own tree, the entropy signal fires on dozens of
pre-existing, legitimate test fixtures for the provider/config/redaction/
audit subsystems, none of them a real credential).

```
go test ./internal/build/ -run TestSecretScanGate
go test ./internal/build/ -run TestSecretScanSeededViolation
```

**Red condition:** `TestSecretScanGate` reports any tracked, non-test
file whose content matches a vendor-prefixed API key, a JWT triplet, a
PEM private-key block, a URL-embedded password, or a bearer token.
`TestSecretScanSeededViolation` is the falsifiability proof: it must stay
GREEN, proving the gate can still turn red on a materialized fixture — if
it ever fails, the gate itself is broken, not the tree.

**What this scan does NOT catch**, stated here rather than left implicit:
any `_test.go` file (excluded entirely — this repo's own tests
legitimately carry credential-shaped fixtures across many packages, and
hand-vetting each one was out of this ticket's scope; a real credential
pasted into a test file is not caught here), anything under a `testdata/`
path (same reason), the small named set in
`internal/build.SecretScanExemptions` (documentation/pattern-definition
files carrying an example shape, each with its own reason and a
staleness test), any credential with no known shape and no adjacent
entropy signal, and anything outside the current tracked tree (deleted
history is not retroactively scanned). A green run is proof the scoped
scan found nothing today, never proof no credential was ever committed
anywhere in this repo's history.

The seeded-violation fixture is generated at test time into `t.TempDir()`
from a documented pattern (a vendor key-prefix + 16 alphanumeric
characters, split across two Go string literals in source so no
contiguous credential-shaped run exists in the tracked file itself) and
is never committed to this public repo, matching the identifier sweep's
own rule against ever writing real- or realistic-shaped secret material
into tracked content.

### (c) Identifier sweep — A-T5

The personal-identifier sweep, `internal/build/sweep.go` plus the
`hygiene` CI lane and the local pre-push hook. Its pattern source is
NEVER a tracked file (`docs/developer/hygiene.md`'s pattern-source law):
an untracked local file (`CASCADE_IDENTIFIER_PATTERNS_FILE`, default
`.claude/hygiene/identifier-patterns.txt`) on a dev machine, or the
masked CI variable `CASCADE_IDENTIFIER_PATTERNS`. An unreadable or
missing source fails closed — it blocks, it never silently skips.

```
CASCADE_HYGIENE_RUN=1 go test ./internal/build/ -run TestIdentifierSweepGate_Live
```

**Red condition:** any reported violation, or a hard error from an
unreadable pattern source. See `docs/developer/hygiene.md` for the full
gate description, the fixture-path exclusion and the pre-push hook
install step.

## Recorded item — SECURITY.md disclosure-SLA concreteness

S-55.T5's forged contract assigns this gate one item beyond the plan's
three automated checks: SECURITY.md's disclosure-SLA values (response
time, fix time, and similar concrete numbers) are "implementation-time
policy content owned at the AA/S-56.T3 launch gate + Art.8.10 owner
sign-off" — this checklist records the item and its check command;
neither this ticket's code nor its own checks assume SECURITY.md exists
(no dependency edge exists between the two sprints).

```
grep -nE '\b(TBD|TODO|FIXME|\[x\] days?|\[x\] hours?)\b' SECURITY.md
```

**Red condition:** the command finds a placeholder, OR `SECURITY.md` does
not yet exist. As of this ticket landing, `SECURITY.md` has not shipped
(S-55.T5 is a separate ticket) — this item is UNMET today, recorded
honestly rather than marked satisfied, and stays unmet until S-55.T5
lands concrete values and the owner reviews them at gate time.

## Owner sign-off (Art.8.10)

Every gate run above ends here. The owner completes this block by hand;
no automation marks it done, and no ticket may fill it in on the owner's
behalf.

```
Gate run: [ ] pre-rc-cut   [ ] pre-promotion
Date:
Commit:
(a) License report:   [ ] green   [ ] red — see:
(b) Secret scan:       [ ] green   [ ] red — see:
(c) Identifier sweep:  [ ] green   [ ] red — see:
SECURITY.md SLA item:  [ ] concrete, no placeholders   [ ] file absent / unmet
Owner signature:
```

**Current honest state, as of this ticket:** (a) and (c) are real,
implemented gates that this ticket did not modify. (b) is implemented by
this ticket, with a seeded-violation proof that it can fail. The
SECURITY.md SLA item is UNMET — the file does not exist yet. This
checklist records conditions; it does not, and must not, mark any of them
satisfied on the owner's behalf.
