# MASTER-SECURITY.md — Cascade Security Controls

**Purpose:** Registry of every security control implemented in Cascade.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P2/P3 (E-07 Security & Privacy Baseline) plan

| Control | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|
| IPC auth tokens | Shared-secret auth for daemon IPC connections | 🔲 Planned | P2 | T-P2-E07-* |
| API key keychain storage | OS keychain storage for AI provider API keys | 🔲 Planned | P3 | T-P3-E05-* |
| Code signing (macOS) | Apple Developer certificate signing for .app and CLI | 🔲 Planned | P3 | T-P3-E06-* |
| Code signing (Windows) | SignPath signing for Windows MSI/EXE | 🔲 Planned | P3 | T-P3-E06-* |
| GPG signing (Linux) | GPG-signed .deb/.rpm packages | 🔲 Planned | P3 | T-P3-E06-* |
| Update signature verification | Ed25519 signature check on update manifest + binary | 🔲 Planned | P3 | T-P3-E06-* |
| WASM sandbox | wasmtime capability-restricted sandbox for plugins | 🔲 Planned | P4 | T-P4-E04-* |
| TLS for HTTP transport | TLS on HTTP/SSE MCP transports when remote | 🔲 Planned | P4 | T-P4-E03-* |
| Redacted settings snapshot | settings.json /api/gci/settings redacts secret fields | 🔲 Planned | P3 | T-P3-E02-* |
