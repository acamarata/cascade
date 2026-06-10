# MASTER-TRANSPORTS.md — Cascade MCP Transport Implementations

**Purpose:** Registry of every MCP transport implemented in cascade-mcp.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P4/E-03 plan

| Transport | Module | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| Transport trait | crates/cascade-mcp/src/transport/mod.rs | Base trait: send/recv/close | 🔲 Planned | P4 | T-P4-E03-* |
| stdio | crates/cascade-mcp/src/transport/stdio.rs | stdin/stdout line-delimited JSON-RPC for subprocess MCPs | 🔲 Planned | P4 | T-P4-E03-* |
| Unix socket | crates/cascade-mcp/src/transport/unix.rs | Unix domain socket for local MCP IPC | 🔲 Planned | P4 | T-P4-E03-* |
| TCP | crates/cascade-mcp/src/transport/tcp.rs | TCP socket transport for remote MCP | 🔲 Planned | P4 | T-P4-E03-* |
| HTTP | crates/cascade-mcp/src/transport/http.rs | HTTP POST stateless MCP exchange | 🔲 Planned | P4 | T-P4-E03-* |
| SSE | crates/cascade-mcp/src/transport/sse.rs | Server-Sent Events: HTTP for server→client notifications | 🔲 Planned | P4 | T-P4-E03-* |
