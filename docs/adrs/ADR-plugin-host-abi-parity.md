# ADR: Plugin host-ABI parity across builtin, process, and wazero runtimes

- Status: Accepted (spike finding — ABI is sound, no amendment required)
- Ticket: P1-E14-W3-S30-T6 (12-QUALITY-CONSTITUTION.md Art.12 risk spike,
  no dependency on and no scope claim over Epic O)
- Consumed by: O/S-32.T1 (host-ABI definition) and O/S-32.T2 (conformance
  suite implementation) at their own scheduling. O/S-32.T1 remains the
  sole owner of the shipped `internal/plugins/hostfn.go`; this spike
  creates none (R-21.276).
- Evidence: `internal/plugins/hostfn_spike_test.go`,
  `internal/plugins/adapter_{builtin,process,wazero,wazero_methods}_spike_test.go`,
  `internal/plugins/conformance_test.go`,
  `internal/plugins/testdata/conformance/` (WAT source, compiled wasm,
  proc_stub subprocess), `internal/plugins/testdata/README.md`
  (provenance).

## The question

Before O/S-32 (W4) commits to a host-ABI implementation, this spike
answers: can the builtin, process-RPC, and wazero runtimes all pass the
SAME conformance suite for the full seven-function ABI v1 host surface
(HostHTTP, HostStorage, HostLog, HostStream, HostSecretRef, HostEventEmit,
HostToolRegister)?

## Answer, stated plainly

**Yes. All three runtimes pass the same 21-subtest conformance suite (7
host functions x 3 runtimes) plus every error-path subtest, with no
per-runtime special-casing in the test assertions themselves.**
`TestHostFnConformance` in `internal/plugins/conformance_test.go` drives
one shared `HostFn` interface against three real, independent
implementations, and every one of the 21 success-path subtests plus the 9
error-path subtests (3 error paths x 3 runtimes) passed under
`-race` on this development machine (darwin/arm64).

## Per-runtime, per-function pass/fail table

| Host fn | builtin | process | wazero |
|---|---|---|---|
| HostHTTP | PASS | PASS | PASS |
| HostStorage | PASS | PASS | PASS |
| HostLog | PASS | PASS | PASS |
| HostStream | PASS | PASS | PASS |
| HostSecretRef | PASS | PASS | PASS |
| HostEventEmit | PASS | PASS | PASS |
| HostToolRegister | PASS | PASS | PASS |
| nil-input error path | PASS | PASS | PASS |
| oversized-payload error path | PASS | PASS | PASS |
| cancelled-context error path | PASS | PASS | PASS |

## How each runtime was actually exercised

- **builtin**: an in-process Go struct (`builtinAdapter`) implementing
  `HostFn` directly, with no serialization. Records every call for the
  boundary-mutation assertion in `TestHostFnConformance` (7 recorded
  calls, one per method, confirmed non-zero and exact).
- **process**: `buildProcStub` compiles a real, freestanding Go program
  (`testdata/conformance/proc_stub/main.go`) via `go build` into
  `t.TempDir()` and `processAdapter` spawns it as a real OS subprocess,
  speaking one JSON envelope/result pair per line over its actual
  stdin/stdout pipes -- not an in-process fake.
- **wazero**: `wazeroAdapter` loads `testdata/conformance/test.wasm`
  (compiled from `test.wat` via the real WABT `wat2wasm` 1.0.41 toolchain,
  not self-authored bytes -- provenance in `testdata/README.md`) through
  the real `github.com/tetratelabs/wazero` library (v1.12.0, added as a
  direct dependency by this ticket). All seven host functions are
  registered as real wazero host functions under module name `env`;
  each typed Go method call writes a request envelope into the wasm
  module's own linear memory, calls the module's exported wrapper
  function (a real WebAssembly call), which invokes the registered host
  import, which writes a response envelope back into the same linear
  memory for Go to read. This is a genuine cross-boundary round trip, not
  a Go-side shortcut that merely reuses the builtin adapter's return
  value.

## ABI friction observed

None that required a design change. The shared `(i32 ptr, i32 len) ->
i32 (responseLen)` calling convention, with a fixed-offset scratch
request buffer and a fixed-offset scratch response buffer in linear
memory, was sufficient for every one of the seven host functions without
per-function special-casing on the wasm side. The three shared
preconditions (nil input, oversized payload, cancelled context) were
implemented identically across all three adapters as a single
`checkCommon` helper, confirming the ABI v1 error-path contract is
runtime-agnostic.

One real constraint worth naming for O/S-32.T1's design: this spike's
memory protocol reserves two fixed 64KiB pages (request, response) and is
NOT safe for concurrent in-flight calls against the same module instance
(a second call would overwrite the first's scratch buffers before it is
read). This spike never needed concurrency -- the conformance suite calls
each method sequentially -- but O/S-32.T1's real ABI, if it wants
concurrent host calls against one plugin instance, needs either a
per-call offset allocator or a length-prefixed ring buffer instead of two
fixed offsets. This is a genuine, positive constraint discovered by
building the real thing, not a hypothetical.

## Platforms

wazero is pure Go (no CGO), so its compiled artifact does not vary by
platform in a way that changes correctness. `go build ./internal/plugins/...`
and `go vet ./internal/plugins/...` (both with `-tags spike`) were
cross-compile-verified for linux/amd64, linux/arm64, darwin/amd64 and
windows/amd64 in this environment -- all four succeeded. **Runtime
execution of `TestHostFnConformance` was only actually performed on
darwin/arm64** (this development machine); this environment has no linux
host, no additional darwin hardware, and no emulator to execute the
cross-compiled binaries, so this ADR does not claim runtime verification
on linux/amd64, linux/arm64, darwin/amd64, or windows/amd64 -- only that
they compile. O/S-32.T2 should run the conformance suite on real
linux/amd64 and linux/arm64 hosts (its CI matrix) and on a real Windows
host before treating those platforms as verified.

The process adapter's Windows path (binary name gets a `.exe` suffix,
`buildProcStub`) was written to genuinely exercise the subprocess route
rather than carry a refusal, per Art.5 -- but, per the platform note
above, this was compile-verified only, not executed on Windows.

## DECISION

**ABI is sound — no amendment to O/S-32.T2 required** for the seven
host-function surface, the shared calling convention, or the shared
error-path contract. The one recommendation for O/S-32.T1's design is the
concurrency note above (per-call memory allocation rather than two fixed
scratch offsets), which is an implementation detail of the real ABI, not
a change to the function set or ABI v1's error semantics this spike was
asked to de-risk.

## Conformance suite skeleton for O/S-32.T2

`internal/plugins/testdata/conformance/` (`test.wat`, `test.wasm`,
`proc_stub/main.go`, `proc_stub/main_test.go`) plus the three adapter
files and `conformance_test.go` are left as the skeleton O/S-32.T2 builds
on. Per R-21.276, this ticket's `HostFn` interface, all three adapters,
and every host-function type live only in `_test.go` files or under
`testdata/`; `internal/plugins/hostfn.go` does not exist yet and remains
O/S-32.T1's file to create.

## Not in scope

No dependency edge to Epic O is added by this ticket. No RPC or CLI
surface, no plugin manifest/loading changes, and no production
`internal/plugins/hostfn.go` are part of this spike.
