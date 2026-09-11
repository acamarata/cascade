# internal/plugins/wasm fixture provenance

Ticket: P1-E15-W4-S32-T1 (12-QUALITY-CONSTITUTION.md Art.2 external-contract
requirement: real WASM binary fixtures, never a self-authored Go stub).

## fixture.wasm

Compiled from `fixture.wat`, hand-authored WebAssembly Text, using the
real WABT toolchain.

- Tool: `wat2wasm` (WebAssembly Binary Toolkit / WABT)
- Tool version: `1.0.41`
- Compilation command: `wat2wasm fixture.wat -o fixture.wasm`
- WebAssembly binary format version: `0x1` (MVP core wasm), no post-MVP
  extensions used
- Compilation date: 2026-09-07

The module imports the seven ABI v1 host functions under module name
`env` (`host_http`, `host_storage`, `host_log`, `host_stream`,
`host_secretref`, `host_eventemit`, `host_toolregister`, all
`(i32, i32) -> i32`), re-exports one wrapper per function
(`call_host_http`, etc.) plus its linear memory (2 pages, 128KiB), and
exports an `i32` global `cascade_abi_version` set to `1`, matching
`HostABIVersion()` in `runtime.go`. This is the same calling convention
established by the O/S-30.T6 spike (`internal/plugins/testdata/conformance/`)
and the S-32.T2 conformance suite builds on; this ticket adds the version
global those fixtures did not need.

`runtime_test.go`'s `TestLoadModule_RealWASMFixture` loads this module
through the real `github.com/tetratelabs/wazero` library (`v1.12.0`),
registers the seven host functions as real wazero host functions, and
calls an exported wrapper through a real WebAssembly call boundary.

## abi_mismatch.wasm

Compiled the same way from `abi_mismatch.wat`: same import/export shape,
but `cascade_abi_version` is `999`, deliberately below the loader's
accepted set. Used only by `TestABIVersionMismatch_HardError` to prove
`Load` returns a typed error rather than accepting a mismatched module.

## resource.wasm

Compiled the same way from `resource.wat`: declares memory with min 2 /
max 4096 pages and exports `grow_memory` (calls `memory.grow` directly,
returning wasm's own -1-on-refusal so `resource_limits_test.go` can
assert against the real, engine-level `WithMemoryLimitPages` ceiling),
`spin_fuel` (calls `host_log` in a loop, to exhaust the fuel/call-count
budget), and `spin_forever` (an unconditional loop with no host calls, to
exercise the wall-clock cap via wazero's `WithCloseOnContextDone`).

## testdata/fuzz/FuzzLoadModule/seed001

A byte-for-byte copy of `fixture.wasm`, seeding `FuzzLoadModule`
(loader_test.go) with one known-valid real WASM binary per §5.7's
seed-corpus requirement.

## Verified platforms

wazero is pure Go (no CGO); module instantiation was executed and
verified on darwin/arm64 (this development machine). `go build`,
`go vet`, `CGO_ENABLED=0 go build`, `GOOS=linux go build`, and
`GOOS=windows go build` were cross-compile-verified (build only) for
this package in this environment, which has no linux or Windows host and
no emulator to execute the resulting binaries — runtime execution is
claimed only for darwin/arm64.
