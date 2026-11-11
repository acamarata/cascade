# OpenCode Driver

## Overview
This driver implements `contract:opencode-run-driver` for the Cascade agent harness. It spawns `opencode run --format json --dir <worktree>` and maps its JSON event stream to `AgentEvent` with an injected clock. Each native part is passed through as `NativeEvent`.

## Event Mapping
Every event type present in the recorded fixture is mapped to an `AgentEvent` kind. Unknown event types return `ErrUnknownEventKind` (typed). Native event parts are passed through unchanged.

## Security & Custody
- The driver never inspects the vendor's TLS egress (R-21.177 limit).
- No network listeners are bound; `--share`, `--attach`, and `--port` are forbidden.
- The spawn environment is built via `DriverEnvAllowlist` and the custody shim.
- The `compat_key` credential class is read from the daemon environment and never written to disk.

## Listeners
Any listening socket disclosed during capture is routed through the per-spawn proxy review and never hidden. The driver itself binds no ports.

## Provenance
The recorded fixture `testdata/run.jsonl` was captured from a live `opencode run --format json` session. The GLM compat key was read by the daemon from its environment and never written to a file or fixture.
