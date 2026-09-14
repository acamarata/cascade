# internal/plugins/conformance fixture provenance

Ticket: P1-E15-W4-S32-T2 (12-QUALITY-CONSTITUTION.md Art.2 external-contract
requirement: real guest artifacts for a runtime that executes real guest code,
never a self-authored Go stub).

## fixtures/abi_v1.json

Hand-authored JSON, one array of 14 entries (7 ABI v1 host functions x
happy-path + error-path). Each entry's `wasm_request` is the exact wire
shape `internal/plugins/wasm`'s host_abi_*.go handlers decode
(`[]byte` fields are base64, matching Go's own `encoding/json` convention
for `[]byte`). Entries that the process runtime's real, narrower wire
protocol also observes carry `process_method` (the literal
`process.Notification.Method` string `hostcalls.go` recognizes),
`process_params` (that protocol's own field shapes -- genuinely different
field names than `wasm_request` for `host_storage`/`host_http`/
`host_secretref`, recorded as a `note` rather than forced into a false
match), `process_grant_profile` ("granted" or "denied", selecting which
fixed `host.Grants`/policy/broker profile `harness_process_test.go`
builds), and `expect_process_allowed`. `host_log` fixtures carry none of
these: the process runtime has no wire method for `host_log` at all
(`hostcalls.go`'s own method vocabulary), which is itself the recorded
finding for that host function, not a gap in the fixture set.

- Tool: hand-authored to the real `wasm.*Request`/`wasm.*Response` and
  `process.Notification` wire shapes (no external RPC client), the same
  convention `internal/plugins/host/testdata/fixtures/README.md`
  documents for its own fixtures.
- Version: `cascade.plugin` host ABI v1 (05-PEWS-PLAN-W4-W6.md §Epic O
  preamble's host-function list).
- Date captured: 2026-09-14.

## guests/fixture.wasm

The SAME real, WABT-compiled WASM guest artifact `internal/plugins/wasm`'s
own P1-E15-W4-S32-T1 ticket built and verified
(`internal/plugins/wasm/testdata/README.md` carries the full compilation
record: `wat2wasm 1.0.41`, MVP core wasm, compiled 2026-09-07). Copied
byte-for-byte rather than recompiled, verified identical by SHA-256:

```
3a2212654cabd5b0eee369cc6d45367fc7e9d2ee81cf5de3a3284b13ae71829f  internal/plugins/wasm/testdata/fixture.wasm
3a2212654cabd5b0eee369cc6d45367fc7e9d2ee81cf5de3a3284b13ae71829f  internal/plugins/conformance/testdata/guests/fixture.wasm
```

The module exports one wrapper per ABI v1 host function
(`call_host_log`, `call_host_storage`, `call_host_http`, `call_host_stream`,
`call_host_secretref`, `call_host_eventemit`, `call_host_toolregister`)
plus its linear memory, and an `i32` global `cascade_abi_version` set to
`1`. `harness_wazero_test.go`'s `wasmHarness` loads it through the real
`github.com/tetratelabs/wazero` runtime and calls each wrapper through a
genuine WebAssembly call boundary -- request and response envelopes cross
via the module's own linear memory, never a Go-side shortcut.

## The process runtime's "guest": a real forked `/bin/sh`

The process leg of this suite (`harness_process_test.go`) does not use a
compiled Go stub binary. It follows the SAME real-counterpart pattern
`internal/plugins/host/boundary_integration_test.go`'s
`TestHostBoundary_RealPluginABI` already established and
`internal/plugins/process/runtime_extra_test.go`'s
`TestProcessRuntime_RealCounterpart` also uses: `process.ProcessRuntime.Launch`
spawns a genuine `/bin/sh -c '<script>'` OS process, which answers the real
`cascade.hello` handshake and then writes one real, literal
`process.Notification` JSON-RPC frame to its own stdout -- transiting the
package's real `transport.go` framing and `hostcalls.go`'s real
notification-consumption loop, not a shortcut through either. This is a
real guest process, not a self-authored stub, even though its "source" is
a one-line shell script rather than a compiled binary: the ABI contract
this suite verifies is the wire protocol and the host-side boundary
check, not the guest's own implementation language.

## Verified platforms

The wazero leg is pure Go (no CGO); the shell leg needs a POSIX `sh`. Both
were executed and verified on darwin/arm64 (this development machine).
`go build`/`go vet ./internal/plugins/conformance/...` were
cross-compile-verified (build only) for linux/amd64 and windows/amd64 in
this environment, which has no linux or Windows host and no emulator to
execute the resulting binaries. `TestConformance_ProcessRuntime` skips
explicitly (with a CI-asserted skip count, never a silent pass) on
`windows`, where `process.ProcessRuntime.Launch` refuses unconditionally
(`runtime_windows.go`); the wazero and builtin legs carry no such
platform gate and are expected to run for real on every GOOS the repo's
`build-test` CI matrix covers.
