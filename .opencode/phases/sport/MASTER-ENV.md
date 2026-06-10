# MASTER-ENV.md — Cascade Environment Variables

**Purpose:** Registry of every env var used by the cascade binary and daemon.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P2/P3/P4 plan

| Variable | Default | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| CASCADE_TCP_IPC_PORT | 9761 | TCP port for daemon IPC server | 🔲 Planned | P2 | T-P2-E01-* |
| CASCADE_SOCKET | ~/.cascade/daemon.sock | Unix socket path for IPC | 🔲 Planned | P2 | T-P2-E01-* |
| CASCADE_CONFIG_DIR | ~/.cascade/ | Root config directory | 🔲 Planned | P2 | T-P2-E01-* |
| CASCADE_INDEX_ROOT | ~/.cascade/index/ | RAG index root directory | 🔲 Planned | P4 | T-P4-E02-* |
| CASCADE_MODEL_DIR | ~/.cascade/models/ | Local LLM model storage | 🔲 Planned | P3/P4 | T-P3-E05-* |
| CASCADE_TEMPLATES_DIR | ~/.cascade/templates/ | Instruction template storage | 🔲 Planned | P3 | T-P3-E04-* |
| CASCADE_APC_PATH | — | Path to App-level (APC) instruction file | 🔲 Planned | P2 | T-P2-E01-* |
| CASCADE_MAX_EMBED_WORKERS | 4 | Max parallel embedding worker threads | 🔲 Planned | P4 | T-P4-E02-* |
| CASCADE_RAG_SHARD_COUNT | 8 | Number of SQLite shards for RAG index | 🔲 Planned | P4 | T-P4-E02-* |
| CASCADE_MCP_URL | — | Override URL for MCP server endpoint | 🔲 Planned | P4 | T-P4-E03-* |
| CASCADE_OTEL_ENDPOINT | — | OpenTelemetry collector endpoint | 🔲 Planned | P3 | T-P3-E06-* |
| CASCADE_AUTO_UPDATE | true | Enable/disable auto-update checks | 🔲 Planned | P3 | T-P3-E06-* |
| CASCADE_UPDATE_CHECK_URL | — | URL for update manifest | 🔲 Planned | P3 | T-P3-E06-* |
| CASCADE_UPDATE_PUBKEY | — | Ed25519 public key for update signature verification | 🔲 Planned | P3 | T-P3-E06-* |
| CASCADE_APPLE_CERTIFICATE | — | Apple Developer certificate for macOS code signing | 🔲 Planned | P3 | T-P3-E06-* |
| CASCADE_GPG_PRIVATE_KEY | — | GPG private key for Linux package signing | 🔲 Planned | P3 | T-P3-E06-* |
| CASCADE_GPG_PASSPHRASE | — | Passphrase for CASCADE_GPG_PRIVATE_KEY | 🔲 Planned | P3 | T-P3-E06-* |
| CASCADE_SIGNPATH_API_TOKEN | — | SignPath API token for Windows code signing | 🔲 Planned | P3 | T-P3-E06-* |
| CASCADE_SIGNPATH_ORGANIZATION_ID | — | SignPath organization ID | 🔲 Planned | P3 | T-P3-E06-* |
