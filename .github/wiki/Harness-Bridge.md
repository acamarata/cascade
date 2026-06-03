# Harness Bridge

The harness bridge (`crates/cascade-daemon/src/harness_bridge.rs`) provides detection and single-shot invocation of AI coding harnesses from within the Cascade daemon.

## Supported harnesses

| Harness | Binary | Invocation flag |
|---|---|---|
| Claude Code | `claude` | `claude -p '<prompt>'` |
| OpenCode | `opencode` | `opencode run '<prompt>'` |

## HarnessStatus

Returned by `detect()` and `detect_all()`:

```json
{
  "harness": { "kind": "claude_code" },
  "running": true,
  "pid": 12345,
  "binary_path": "/usr/local/bin/claude"
}
```

Fields:
- `harness` — which harness (`claude_code`, `open_code`, or `unknown` with a `String` payload)
- `running` — whether a process with the harness binary name is currently running
- `pid` — PID of the first matching process, or `null`
- `binary_path` — resolved path to the binary, or `null` if not on PATH

## IPC endpoint

`harness.detect` — returns a JSON array of `HarnessStatus` for all known harnesses.

Request (wrapped in the standard auth envelope):

```json
{ "method": "harness_detect" }
```

Response:

```json
[
  { "harness": { "kind": "claude_code" }, "running": true, "pid": 12345, "binary_path": "/usr/local/bin/claude" },
  { "harness": { "kind": "open_code" }, "running": false, "pid": null, "binary_path": null }
]
```

## Invocation

`invoke(harness, prompt, timeout_secs)` spawns the harness CLI with a single prompt.
Returns `InvocationResult { stdout, stderr, exit_code, duration_ms }` or a typed error:
- `BinaryNotFound` — binary not on PATH
- `Timeout` — process ran longer than `timeout_secs`
- `Io` — spawn or read failure

## CLI command

`cascade harness status` — displays the status of all detected harnesses in a human-readable table.

```
$ cascade harness status
HARNESS    PID        RUNNING    BINARY
ClaudeCode -          false      -
OpenCode   -          false      -
```

Flags:
- `--json` — output as JSON instead of a formatted table

## P2 scope

This module is the P2 foundation layer. Full cross-repo dispatch and teaching harnesses to use Cascade via MCP are P4 scope.

## P4 roadmap

- OC deeper integration via open-source OC hook API (P4)
