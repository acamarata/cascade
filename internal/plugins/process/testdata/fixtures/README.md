# Handshake fixtures

`handshake_hello.json` and `handshake_ack.json` record one cascade.hello /
cascade.hello_ack exchange in the wire shape `internal/plugins/process`
sends and expects (see `types.go`).

- Tool: POSIX `sh` (the same interpreter `TestProcessRuntime_
  RealCounterpart` in `runtime_test.go` spawns as a real, non-repo OS
  process to answer the recorded handshake — no compiled plugin binary
  ships as test fixtures for this ticket).
- Version: the system `/bin/sh` on the machine that captured these
  fixtures (POSIX-compatible `sh`, no version string of its own).
- Date captured: 2026-09-06.

The two files are the literal JSON-RPC 2.0 frames `internal/plugins/
process` transports over stdio: `handshake_hello.json` is the host's
outbound `cascade.hello` request, `handshake_ack.json` is the plugin's
`cascade.hello_ack` response. `FuzzProcessTransport`'s seed corpus draws
from the same two shapes.
