# Error taxonomy

The single error taxonomy for the whole `cascade` binary, per T0 ruling
R-14.2/R-14.3. Package home: [`pkg/cascade`](../../pkg/cascade) — the public
core package holding the kinds, the wire-code tables, and the constructors
and inspection helpers. Call sites read `cascade.ErrNotFound` /
`cascade.Kind`, never a package literally named `errors` (this package is
named `cascade` for exactly that reason).

This is the plan's acceptance artifact for A-T7
(`P1-E01-W1-S01-T7`): the kinds table, the codes, and all three wire
mappings. `B/S-02.T1` (storage family interfaces) and `D/S-06.T3` (JSON-RPC
framework) both consume this taxonomy with **zero additions** — domain
sentinels live in their owning package (see [Sentinel values](#sentinel-values-and-domain-errors)
below), never here.

## Kinds (frozen, R-14.3)

The enumeration below is **closed**: exactly 14 members, zero additions or
omissions without a T0 amendment to R-14.3. `cascade.AllKinds()` returns
them in this order, and a test (`TestKindEnumerationClosed`) locks the count
and order.

| Kind constant | Wire name | Meaning |
|---|---|---|
| `KindNotFound` | `not-found` | A named resource does not exist. |
| `KindInvalidInput` | `invalid-input` | Caller-supplied input failed validation. |
| `KindConflict` | `conflict` | A state conflict (optimistic-concurrency mismatch, duplicate creation). |
| `KindUnavailable` | `unavailable` | A dependency is temporarily unreachable; retry is reasonable. |
| `KindTimeout` | `timeout` | An operation exceeded its deadline. |
| `KindCanceled` | `canceled` | The caller canceled the operation (context cancellation, SIGINT). |
| `KindPermissionDenied` | `permission-denied` | The caller lacks the required rights; no elevation offered. |
| `KindElevationRequired` | `elevation-required` | The operation needs an elevation flow (fresh attestation) first. |
| `KindPolicyDenied` | `policy-denied` | Policy evaluation (`internal/policy`) refused the operation. |
| `KindCapabilityDenied` | `capability-denied` | The active tier does not sell this capability at all. |
| `KindQuotaExhausted` | `quota-exhausted` | A purchased capability's quota is spent for the current window. |
| `KindUnsupported` | `unsupported` | Recognized but not implemented on this platform/build/config. |
| `KindIntegrity` | `integrity` | A verification step (checksum, signature, schema) failed. |
| `KindInternal` | `internal` | Unclassified internal failure; the wire-mapping fallback kind. |

`capability-denied` and `quota-exhausted` are **distinct by design**: a
capability a tier does not sell must never be retried or rotated as if it
were a spent quota (PCI `provider-capability-flags`, 2026-09-01).

## Constructing and inspecting taxonomy errors

```go
import "github.com/acamarata/cascade/pkg/cascade"

// Construct
err := cascade.New(cascade.KindNotFound, `provider "openai" not configured`)
err  = cascade.Newf(cascade.KindInvalidInput, "field %q out of range", "count")
err  = cascade.Wrap(cascade.KindUnavailable, cause, "writing snapshot")
err  = cascade.Wrapf(cascade.KindTimeout, cause, "after %d attempts", 3)

// Inspect (errors.Is/As-compatible; kind survives internal/ wrapping,
// including a plain fmt.Errorf("...: %w", err))
if errors.Is(err, cascade.ErrNotFound) { ... }
if kind, ok := cascade.KindOf(err); ok { ... }
if cascade.HasKind(err, cascade.KindTimeout) { ... }
```

Every taxonomy error is a `*cascade.Error{Kind, Msg, Err}`. `errors.Is`
between two taxonomy errors compares **only the Kind** — this is what lets
`errors.Is(err, cascade.ErrNotFound)` succeed for *any* `KindNotFound` error
regardless of message, and what lets an `internal/` package wrap freely
(`fmt.Errorf("dispatch failed: %w", err)`) without the caller losing the
ability to recover the kind.

### Sentinel values and domain errors

`pkg/cascade` ships one generic sentinel per kind (`cascade.ErrNotFound`,
`cascade.ErrConflict`, ...) for convenience at call sites. **Domain-specific
sentinel error values** (for example storage's `ErrDomainOwned`) belong in
their **owning package**, each wrapping exactly one frozen kind from this
taxonomy — they are never added to `pkg/cascade`. This is what keeps the
taxonomy consumable by `B/S-02.T1` and `D/S-06.T3` with zero additions.

## Wire mappings

Every kind maps to exactly one CLI exit code, one JSON-RPC error code, and
one plugin-RPC error code. The three tables are **total** (every kind is
covered) and **non-overlapping** (no two kinds share a code within a table);
`TestTaxonomyTablesTotalAndNonOverlapping` in `pkg/cascade` enforces this.

### CLI exit codes

`0` is success, not a kind. `130` follows the SIGINT convention
(128 + signal 2) rather than continuing the sequential numbering, matching
how a shell reports a process killed by Ctrl-C. `cascade.ExitCode(err)` is
the single entry point a `cmd/` composition root calls, once, at the
boundary to `os.Exit`.

| Code | Meaning | Kind |
|---|---|---|
| 0 | ok | — (no error) |
| 1 | internal | `KindInternal` (also the fallback for a non-taxonomy error) |
| 2 | invalid-input | `KindInvalidInput` |
| 3 | not-found | `KindNotFound` |
| 4 | conflict | `KindConflict` |
| 5 | unavailable | `KindUnavailable` |
| 6 | timeout | `KindTimeout` |
| 7 | permission-denied | `KindPermissionDenied` |
| 8 | elevation-required | `KindElevationRequired` |
| 9 | policy-denied | `KindPolicyDenied` |
| 10 | capability-denied | `KindCapabilityDenied` |
| 11 | quota-exhausted | `KindQuotaExhausted` |
| 12 | unsupported | `KindUnsupported` |
| 13 | integrity | `KindIntegrity` |
| 130 | canceled | `KindCanceled` (SIGINT convention) |

### JSON-RPC error codes

The JSON-RPC 2.0 spec reserves `-32768..-32600` for protocol-defined errors
plus `-32000..-32099` as an implementation-defined "server error" band. This
taxonomy's codes sit at the top of that server-error band,
`-32013..-32000`, entirely outside the spec-reserved range.
`cascade.NewRPCError(err)` builds the `{code, message, data}` envelope
`D/S-06.T3` marshals into a JSON-RPC error response.

| Code | Kind |
|---|---|
| -32000 | `KindInternal` (also the fallback for a non-taxonomy error) |
| -32001 | `KindNotFound` |
| -32002 | `KindInvalidInput` |
| -32003 | `KindConflict` |
| -32004 | `KindUnavailable` |
| -32005 | `KindTimeout` |
| -32006 | `KindCanceled` |
| -32007 | `KindPermissionDenied` |
| -32008 | `KindElevationRequired` |
| -32009 | `KindPolicyDenied` |
| -32010 | `KindCapabilityDenied` |
| -32011 | `KindQuotaExhausted` |
| -32012 | `KindUnsupported` |
| -32013 | `KindIntegrity` |

### Plugin RPC

Plugin RPC reuses the JSON-RPC table **verbatim** — same codes, same shape.
`cascade.PluginRPCError` is a type alias for `cascade.RPCError`, and
`cascade.NewPluginRPCError(err)` is `cascade.NewRPCError(err)` under a
plugin-facing name, so the two protocols cannot drift apart into separate
code tables.

## Boundary lint

Rule: **taxonomy kinds at API boundaries, internal wrapping free.** Exported
API surfaces — `pkg/` and `cmd/` composition — must return a
`*cascade.Error` (directly or via one of this package's constructors), never
a raw `fmt.Errorf(...)`, `errors.New(...)`, or `errors.Join(...)` value.
`internal/` packages may wrap however they like; the taxonomy's own
inspection helpers (`errors.Is`/`cascade.KindOf`) see through that wrapping.

The gate lives in `internal/build` (`boundary_test.go` +
`boundary_seeded_test.go`, run by `go test ./internal/build/...`): an AST
scanner walks `pkg/` and `cmd/` (skipping `_test.go` files and `testdata/`)
for `fmt.Errorf`/`errors.New`/`errors.Join` call expressions, resolving each
file's import aliases first so an aliased import (`import ferrors "fmt"`
then `ferrors.Errorf(...)`) still resolves to `fmt.Errorf`. A dot-import of
`fmt` or `errors` anywhere in a scanned tree is rejected outright at the
import declaration, independent of how the dot-imported names are used,
since a dot-imported call site is not a `*ast.SelectorExpr` and cannot be
matched by selector at all.

- **Real tree** (`TestBoundaryLint_RealTree`) must find zero violations —
  zero denied calls and zero dot-imports of `fmt`/`errors`.
- **Seeded fixtures** under
  `internal/build/testdata/seeded-violations/boundary/` deliberately
  contain one case per denied construct, each proven red by its own test:
  `violation.go` (`TestBoundaryLint_SeededViolation`, the original
  `fmt.Errorf` + `errors.New` case), `alias_violation.go`
  (`TestBoundaryLint_SeededViolation_ImportAlias`, `fmt.Errorf` reached
  through an import alias), `dotimport_violation.go`
  (`TestBoundaryLint_SeededViolation_DotImport`, a dot-imported `errors`),
  and `join_violation.go`
  (`TestBoundaryLint_SeededViolation_ErrorsJoin`, `errors.Join`).

### What this lint does and does not prove (R-14.120)

The boundary lint is an AST scan of `pkg/` and `cmd/` non-test source. It
proves **"no raw-error-constructor call is written directly in `pkg/` or
`cmd/` source, under its own name, an alias, or a dot-import."** It does
**not** prove, and cannot by construction of what an AST scan of those two
trees can see, the stronger claim "no raw error value can reach a caller of
`pkg/` or `cmd/`":

- A raw error minted inside `internal/` with `fmt.Errorf`/`errors.New`/
  `errors.Join` — all of which `internal/` is free to use per this
  document's Rule above — and then **returned unchanged through a `pkg/` or
  `cmd/` boundary** is invisible to this scanner: the constructor call
  itself is not textually present in the scanned trees, only its
  already-built value flowing through a return statement, and the scanner
  does not trace values across package boundaries.
- A **bespoke type that implements the `error` interface directly** (no
  `fmt`/`errors` constructor call anywhere) produces no AST node this lint
  watches for at all.

Per **R-14.118**, closing that class of evasion is the responsibility of
the ticket that owns each `pkg`/`cmd` boundary — via `cascade.Wrap` at the
exact point where an `internal/` error crosses out — not of this lint. Do
not read a green `TestBoundaryLint_RealTree` as proof that every error
surfaced by `pkg/`/`cmd/` carries a taxonomy kind; read it only as proof
that no raw constructor call is written in those trees' own source.

## Consumer rules (for B and D)

- `B/S-02.T1` (storage family interfaces): declare storage-domain sentinels
  (e.g. `ErrDomainOwned`) in the storage package, each wrapping exactly one
  kind from this table. Do not add new kinds.
- `D/S-06.T3` (JSON-RPC framework): use `cascade.NewRPCError(err)` to build
  every error envelope; the code table above is authoritative and the
  round-trip tests (`D/S-06.T3`'s own real-counterpart tests) are the place
  that verifies wire behavior against a real JSON-RPC client.
- `D/S-06.T5` (CLI exit-code output contract): use `cascade.ExitCode(err)`
  as the single source of process exit status; do not hand-roll exit-code
  switches elsewhere.
