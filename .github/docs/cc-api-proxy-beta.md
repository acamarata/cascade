# CC API Proxy — Beta Risk Document

**Status: EXPERIMENTAL — OFF BY DEFAULT**
**Ticket: E-P9-06**
**Risk level: HIGH — read this document before enabling**

---

## What it is

`cascade ccapi` is an experimental HTTP+SSE bridge that wraps the interactive
Claude Code CLI (`claude`) to expose a `/v1/messages`-compatible API endpoint
on `http://127.0.0.1:7190`.

When enabled and started, it:
1. Accepts `POST /v1/messages` requests (Anthropic Messages API format).
2. Forwards the last `user` message to the CC process driver.
3. Streams the response back as Server-Sent Events (SSE).

A token-bucket quota guard (default: 20 req/min) limits request rate.

---

## Why it is disabled by default

### Risk 1: Anthropic Terms of Service

Claude Code subscriptions are licensed for **interactive use only**. The
Anthropic Terms of Service do not permit automated or programmatic access
via the Claude Code CLI subscription tier.

Using this bridge for non-interactive (automated) requests may violate those
terms. Anthropic may suspend your Claude Code account without notice.

**You use this feature at your own risk and sole responsibility.**
Cascade makes no representation that use of this bridge is permitted.

### Risk 2: Brittle output parsing

The real PTY driver (feature `live_cc`) must parse Claude Code's terminal
output to extract model responses. CC's terminal output format is:

- **Not a public API** — it can change on any CC release without notice.
- **ANSI-escape-heavy** — stripping escape sequences is error-prone.
- **Prompt-detection-fragile** — detecting turn boundaries requires heuristics
  that may fail if CC changes its prompt format.

Any CC update can silently break the bridge. There is no automated test
against a real CC process in CI.

### Risk 3: Local port exposure

The bridge listens on `127.0.0.1:7190` with **no authentication**. Any
process running on your local machine can reach it. Do not expose port 7190
through a firewall or reverse proxy.

---

## Current implementation status

### What works (always, no feature flag)

- `ProcessDriver` trait and `MockDriver` — fully tested.
- HTTP+SSE bridge logic (axum, SSE streaming) — tested via MockDriver.
- Token-bucket quota guard — tested.
- CC install/auth detection (`cascade ccapi status`) — tested.
- Config flag enforcement (default-off) — tested.
- `cascade ccapi status|start|stop` CLI commands — tested in disabled mode.

### What is a documented stub (feature `live_cc`)

The `LiveCcDriver` that actually spawns and drives the `claude` process is a
**documented stub**. It compiles and implements the `ProcessDriver` trait
interface, but `send_prompt` returns an error rather than driving a real
process.

A full PTY implementation requires platform-specific work
(`libc::openpty`/`nix::pty`) and a robust output parser. This is deferred
to a future iteration. The current build proves the bridge architecture;
the PTY integration is the remaining surface.

To activate the stub: `cargo build --features live_cc`
(This does not make it functional — it compiles the stub and interface only.)

---

## How to enable (read risks above first)

1. Edit `~/.cascade/config.toml`:

   ```toml
   [experimental]
   # WARNING: May violate Anthropic ToS. See .github/docs/cc-api-proxy-beta.md.
   cc_api_proxy = true
   ```

2. Start the bridge:

   ```
   cascade ccapi start
   ```

   Or with a custom port and quota:

   ```
   cascade ccapi start --port 7190 --rpm 10
   ```

3. Check status:

   ```
   cascade ccapi status
   cascade ccapi status --json
   ```

4. Stop:

   ```
   # Ctrl-C in the terminal running cascade ccapi start
   # (the bridge runs in the foreground)
   ```

---

## Local testing without Claude Code

Use the hidden `--mock` flag to start the bridge with a MockDriver
(no CC required, no ToS risk):

```
cascade ccapi start --mock
```

This is useful for testing HTTP clients and SSE handling without a real CC
account or process.

---

## API reference

### POST /v1/messages

Request (subset of Anthropic Messages API):

```json
{
  "model": "claude-code",
  "messages": [
    {"role": "user", "content": "Hello"}
  ],
  "stream": true
}
```

Response: `text/event-stream`

```
event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}

event: message_stop
data: {"type":"message_stop"}
```

On rate limit (`429 Too Many Requests`):

```json
{"error": "rate limit 20 req/min exceeded"}
```

### GET /v1/status

```json
{
  "status": "running",
  "driver": "MockDriver",
  "quota_rpm": 20
}
```

---

## Architecture

```
cascade ccapi start
    │
    └── bridge (axum HTTP, 127.0.0.1:7190)
            │
            ├── POST /v1/messages ──► QuotaGuard ──► ProcessDriver ──► SSE stream
            │
            └── GET  /v1/status   ──► JSON status

ProcessDriver implementations:
    MockDriver   — always compiled, used in tests, --mock flag
    LiveCcDriver — feature=live_cc, documented stub (PTY impl deferred)
```

---

## Changelog

| Date | Change |
|------|--------|
| 2026-06-14 | E-P9-06: Initial scaffold — bridge + MockDriver + quota + auth detection. LiveCcDriver is a documented stub. Default-off. |
