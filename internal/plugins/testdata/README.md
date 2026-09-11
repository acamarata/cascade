# internal/plugins conformance fixture provenance

Spike ticket: P1-E14-W3-S30-T6 (12-QUALITY-CONSTITUTION.md Art.12 risk
spike, Art.2 external-contract requirement).

## test.wasm

`conformance/test.wasm` is compiled from `conformance/test.wat`, a
hand-authored WebAssembly Text source, using the real WABT toolchain --
not self-authored bytes.

- Tool: `wat2wasm` (WebAssembly Binary Toolkit / WABT)
- Tool version: `1.0.41`
- Compilation command: `wat2wasm test.wat -o test.wasm`
- WebAssembly binary format version: `0x1` (MVP core wasm, module magic
  `\0asm`, version `0100 0000`), no post-MVP extensions used
- Compilation date: 2026-09-07

The module imports seven host functions under module name `env`
(`host_http`, `host_storage`, `host_log`, `host_stream`, `host_secretref`,
`host_eventemit`, `host_toolregister`, all `(i32, i32) -> i32`) and
re-exports one wrapper per function (`call_host_http`, etc.) plus its
linear memory (2 pages, 128KiB). `internal/plugins/adapter_wazero_spike_test.go`
loads this module through the real `github.com/tetratelabs/wazero`
library (`v1.12.0`, a direct module dependency after this ticket), registers
the seven host functions as real wazero host functions, and calls each
exported wrapper through a real WebAssembly call boundary -- request and
response envelopes cross via the module's own linear memory, not a Go-side
shortcut.

## Verified platforms

wazero is pure Go (no CGO). Module instantiation and the full
`TestHostFnConformance` suite were executed and passed on this
development machine: darwin/arm64. `go build ./internal/plugins/...` and
`go vet ./internal/plugins/...` were cross-compile-verified (build only,
not executed) for linux/amd64, linux/arm64 and darwin/amd64 in this
environment, which has no linux or additional darwin hardware and no
emulator available to actually run the resulting binaries. This spike does
not claim runtime verification on those three platforms -- only that they
compile. O/S-32.T2 should execute the conformance suite on real
linux/amd64 and linux/arm64 hosts (e.g. in CI) before relying on this
finding for those platforms.

## Process adapter subprocess

`conformance/proc_stub/main.go` is a real, freestanding Go program
compiled on demand by `buildProcStub` (in
`internal/plugins/adapter_process_spike_test.go`) via `go build`, then run
as a real OS subprocess communicating over stdin/stdout with one JSON
envelope/result pair per line. On Windows the compiled binary is given the
`.exe` suffix explicitly so the same subprocess path runs rather than
being refused (Art.5); this was not executed on Windows in this
environment (no Windows host available), only cross-compile-verified.
