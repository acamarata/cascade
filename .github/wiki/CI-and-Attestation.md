# CI and Attestation

## Target selection

`internal/ci` computes which build targets a CI run actually needs, and
decides when it is safe to trust that narrowed set versus falling back to
running everything. This page documents the rules exactly as implemented
by `internal/ci/{affected,affected_go,affected_go_mapping,affected_cmd,
selection}.go` (P1-E32-W6-S65-T1).

### Affected-target computation

`RequirementModel.Affected(ctx, changed)` returns the minimal `[]Target`
set whose transitive inputs include at least one path in `changed`, or
`[]Target{TargetAll}` when that cannot be computed for the model's stack.

- **Go stack** (`affected_go.go`) — one `go list -f
  '{{.ImportPath}}|{{join .Imports ","}},{{join .TestImports
  ","}},{{join .XTestImports ","}}' ./...` subprocess builds the
  worktree's full direct-import graph, folding production imports
  (`.Imports`) and test-file imports (`.TestImports`,
  in-package `_test.go`; `.XTestImports`, an external `_test` package)
  into one edge list per package. A package imported only by another
  package's test file is a real dependent: it must be marked affected
  when the imported package changes, exactly as if the import were in
  production code. `affectedGoTargets` then walks the reverse of that
  graph from each changed path's owning package to collect every
  transitively affected local package (a package always reaches itself).
- **Any other stack** (`affected_cmd.go`) — the operator-configured
  `[ci].affected_cmd` runs through the platform shell with the
  changed-path list on stdin (one path per line); its stdout lines become
  target names. A non-zero exit is the typed `ErrAffectedCmdFailed`, never
  a panic.
- **Fallback** — an unrecognised stack, or a non-Go stack with no
  `affected_cmd` configured, returns `[]Target{TargetAll}`.

### Fail-closed changed-path mapping (Go stack)

Every changed path must map to at least one target, or the WHOLE Go-stack
result forces `[]Target{TargetAll}` — never a partial set that silently
drops the unresolvable path. `affected_go_mapping.go`'s
`changedOwningPackages`/`resolveOwningDir` implement this table:

| Changed path shape | Resolution |
|---|---|
| `go.mod`, `go.sum`, `go.work`, `go.work.sum` (any depth, matched by base filename) | Forces `TargetAll` — these change the module's own dependency graph, which the import-graph subprocess cannot express edges for. |
| Anything under `vendor/` (or `vendor` itself) | Forces `TargetAll` — same reasoning as above. |
| A directory `go list` cannot resolve to a buildable package, and is NOT nested under a `testdata/` directory (a deleted package's directory, an unresolvable path) | Forces `TargetAll` — the directory legitimately held a package once, or was claimed to; a resolution failure here is never read as "no package-level effect". |
| A file at the module root that IS resolvable as a package (a root-level `.go` file, or a non-Go file alongside one) | Maps to the root package normally. |
| A file at the module root with no buildable package there | Forces `TargetAll` — same "unresolvable directory" rule above, applied to the root. |
| A file under a package's `testdata/` directory, at any depth | Maps to the **nearest enclosing package directory** — the resolution walks up one directory at a time (retrying at each level) until an ancestor directory resolves, or the walk leaves `testdata/` entirely (which then forces `TargetAll`, same as any other unresolvable path). `testdata/` is never itself a buildable package by Go's own convention, so it is never itself the "owning package". |
| A non-Go file living directly inside an otherwise-buildable package directory (an embed source, a `.s` file, etc.) | Maps to that package — directory-based resolution does not care which specific file inside the directory changed. |

The **only** legitimate empty affected-target result is an **empty
`changed` input** — zero changed paths returns `[]Target{}` before any of
the above resolution runs at all. Every non-empty `changed` list either
resolves fully to a concrete target set, or forces `TargetAll`; there is
no third outcome.

### SelectTargets (R-21.173)

`SelectTargets(ctx, m RequirementModel, changed, candidateTreeHash,
riskClass) (CIRequirementPlan, error)` is the one target-selection
decision in the tree — streaming CI dispatch (AF/S-65.T2) may run against
a partial affected set, but **acceptance always re-runs this exact
function**, never a re-derived copy of its policy.

`SelectTargets` calls `m.Affected`, classifies the result, then resolves
`Selection`:

- `Selection = full` (with `[]Target{TargetAll}`) when:
  - the affected set is **uncomputable** — the Go-stack fail-closed
    mapping above forced `TargetAll`, or the stack was unrecognised, or
    a non-Go stack had no `affected_cmd` configured;
  - **or** `Affected` returned an **empty, non-`TargetAll`** set while
    `changed` was non-empty — an independent fail-closed guard, checked
    regardless of the Go-stack mapping table above or which producer
    (Go path, `affected_cmd`) built the result. A future mapping bug, or
    an `affected_cmd` that legitimately exits zero with no stdout, must
    never read as "affected, zero targets" — it reads as uncomputable
    and forces the full set;
  - the set is **stale** — computed, but the worktree's current
    `git rev-parse HEAD^{tree}` does not match `candidateTreeHash` (the
    AF/S-65.T2 immutable candidate snapshot identity, R-21.147);
  - `riskClass` is `"high"` or `"critical"` (the AC/S-59.T4 classifier's
    own literal strings) — regardless of how fresh or concrete the
    affected set is.
- `Selection = affected` (with the real target list) only when the
  affected set is a fresh, concrete, non-empty set **and** `riskClass` is
  `"low"` or `"normal"`.

Neither `TargetSelection` nor `AffectedStatus` has a permissive zero
value (06 §5 rule 20): an unset `TargetSelection` resolves to `full`, and
an unset `AffectedStatus` resolves to `uncomputable` — a caller that
forgets to set either field can never silently under-select.

### Reaching the receive gate

The resolved `Selection`, `CandidateTreeHash` and `RiskClass` travel with
the `CIRequirementPlan` into every dispatched CI job. AF/S-65.T4's
attestation payload records `Selection` verbatim as `target_selection`,
so the AF/S-66.T2 receive gate can see exactly which target set a given
CI run actually used — a full run and a narrowed run are distinguishable
after the fact, not just at dispatch time.

## macOS widget lane

The menu bar app and WidgetKit extension under `apps/widget-macos` only build on
macOS, so `.github/workflows/ci.yml` runs them in a `widget` job on the free
`macos-15` runner. It is part of the required status: `ci-gate` needs both the
`changes` job and the `widget` job.

| Part | Rule |
|---|---|
| `changes` job | Decides whether the lane runs. `widget=false` only when every changed file is under `docs/**`, `.github/wiki/**` or is a `**/*.md` file, and nothing changed under `apps/widget-macos/**`. Anything else, including a new Go package, `go.mod` or a workflow edit, runs the lane. |
| Diff base | `github.event.before` on push, the PR base on pull_request. A missing or all-zero base, or one the clone does not hold, gives `widget=true`. |
| `widget` job | SHA-pinned checkout and `actions/setup-go` (from `go.mod`), the pinned Xcode, XcodeGen from a release zip checked against `XCODEGEN_SHA256`, project generation, a `CascadeMenuBar` build, then `CascadeMenuBarTests` and `CascadeWidgetExtensionTests` with Thread Sanitizer on. |
| Evidence | A step fails the job when an xcresult reports zero passed tests or any failed test. Both bundles upload as an artifact when the job fails. |
| `ci-gate` | A skipped `widget` job passes only when `changes` succeeded and reported `widget=false`. A failed or skipped `changes` job, or a skipped `widget` job beside `widget=true`, fails the gate. The checks for every other lane are unchanged. |

The Xcode path, XcodeGen version and its sha256 are `env` values on the `widget`
job. The job log records `xcodebuild -version` and the verified checksum.

Known limit: on a push the diff base is the previous tip of the branch, and `ci.yml`
cancels an older run on the same ref. A docs-only push that lands right after a Swift
push cancels the Swift run and then diffs only itself, so that one push can skip the
lane. Pull requests list the whole PR and do not have this gap.

### Gates that keep the lane honest

`internal/build/widget_ci_test.go` reads `ci.yml` and fails if the lane loses its
shape: action pins, `setup-go`, the required status wiring, the skip rule and the
path filter. The `ci-gate` script runs under bash against every needs-result
combination, and the diff-base step runs against a throwaway git repository. Each
check has a seeded workflow under `internal/build/testdata/seeded-violations/widget-ci/`
that must turn it red.

`internal/build/widget_swift_gate_test.go` keeps the Swift tree inside four rules:
no file over 300 lines, a paired `*Tests.swift` for every production file, no
banned API (`NSAppleScript`, `sh -c`, `String(format:`, `/bin/sh` outside the reauth
command file, `show_project_names`, `Network` or `URLSession` in the extension,
`NSHomeDirectory` in the self-report file), and no stub markers (`TODO`, `FIXME`,
`fatalError("not implemented")`, mock, fake or stub types). Seeded trees live under
`internal/build/testdata/seeded-violations/widget-swift/`.

On Linux and Windows the widget CLI refuses to install. `TestWidgetUnsupportedPlatformRefusal`
in `internal/daemon/service/` proves that in the normal `go test ./...` legs; the
widget lane proves the macOS half.
