# Changelog

All notable changes to cascade are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)

## [Unreleased]

## [0.9.1] - 2026-06-14

Parity Program P11 — foundational correctness. First increment toward Cascade
fully replacing a hand-built multi-harness AI-coding setup.

### Added

- Model-tier registry mapping execution tiers (T0–T3) to provider/model ids,
  configurable per cascade tier via a `[models]` table.
- Always-loaded vs on-demand rule distinction in the resolver: on-demand rules
  render as pointer references in generated harness files instead of being
  inlined, restoring per-turn context-budget discipline.
- Subagent-context prefix injected before each agent provider step for
  prompt-cache-stable multi-agent runs.

### Changed

- `cascade.search` executes the live RRF retrieval pipeline (FTS5) when an index
  is available, returning real hits with citations instead of a placeholder. The
  retriever builds in the background so the MCP `initialize` handshake is never
  blocked on index I/O.
- Chain and orchestration now run independent branches/sub-goals truly
  concurrently, bounded by a CPU-aware semaphore, instead of sequentially.

### Fixed

- cargo-deny bans: pinned internal path-dependency versions on all plugin and
  example crates (resolves the v0.9.0 release pipeline's cargo-deny failure).
- clippy: resolved doc-comment and unused-import warnings workspace-wide so
  `clippy --workspace --all-targets -- -D warnings` is clean.

## [0.9.0] - 2026-06-14

First stable release. Cascade is a FOSS context manager for AI coding
agents: one place to keep instructions, knowledge, and guardrails that
every AI tool you use can read.

### Added

- Six-tier instruction cascade (GCI > PCI > APC > PPC > PRC > PAC) with a
  resolution engine, provenance tracking, and harness-native instruction
  file generation (CLAUDE.md, AGENTS.md) for five AI coding tools.
- `cascaded` daemon (Tokio): JSON-RPC IPC, browser dashboard on
  127.0.0.1:9761 (token-auth writes), Gemini key-pool proxy with
  round-robin rotation, provider health sweeps, usage analytics,
  signed delta self-updates with snapshots and rollback.
- `cascade` CLI: resolve, search, status, template
  (list/apply/diff/upgrade/create/validate/export), mcp (token/setup for
  five client tools), plugin (new/test/list/enable/disable), policy
  (eval/list/add), dispatch (cross-repo agent launcher with policy
  enforcement), docs (inject/list/evict), restore, uninstall, update,
  rollback, cache, context.
- Desktop app (Tauri 2 + React): ten-step onboarding wizard (legacy tool
  scan, AI-assisted config merge, non-destructive archive with restore,
  symlink management, provider connection, Gemini pool provisioning),
  knowledge vault (markdown editor, wikilinks, backlinks, graph view,
  tags, full-text search, daily notes), template browser, persona/prompt
  library with harness injection, context curation with codebase packing,
  project maps (graph, tier tree, PEWS DAG), usage analytics, fleet
  quota gauges, settings for every subsystem.
- Local RAG engine: SQLite + FTS5 keyword index, dense/sparse embeddings
  (fastembed ONNX), optional sqlite-vec KNN and ColBERT-style multivector
  modes, four chunkers (semantic, hierarchical, markdown, tree-sitter
  code-aware), ten document parsers, hybrid RRF retrieval with
  cross-encoder reranking, citations, idempotent ingest, incremental
  indexing, parallel embedding workers, query/embedding caches, eval
  harness (MRR, NDCG, precision/recall@k).
- MCP server (2025-03 spec): resources, tools, prompts, sampling, and
  logging over five transports (unix socket, stdio, HTTP, SSE, TCP
  loopback) with HMAC-SHA256 auth; client auto-setup for five tools;
  token-budgeted context_slice tool.
- WASM plugin system: wasmtime sandbox with fuel metering, memory limits,
  and capability-gated WASI; six plugin kinds; PDK with proc macro,
  project template, and test harness; first-party GitHub/Linear/Jira/
  GitLab data-source plugins and example plugins.
- AI provider layer: twelve cloud adapters behind one trait (streaming,
  fixture-tested), local LLM runners (gemma-2-2b, llama-3.2-3b,
  phi-3-mini via candle), Ollama bridge, OAuth 2.0 PKCE flows with
  keychain token storage, per-task routing, cost estimation.
- Template system: 33 bundled templates (6 tier, 16 stack, 11 shape),
  section-aware apply with provenance stamps, inheritance, semver
  upgrades that preserve user edits.
- Policy guardrails: native deny-list defaults plus WASM policy host;
  dispatch pre-launch enforcement.
- Security: OS keychain storage, loopback-only services, HMAC tokens,
  path traversal guards everywhere user paths flow, ed25519-signed
  updates, SIEGE adversarial test suites.
- Distribution: signed release pipeline (macOS notarization, Windows
  Authenticode via SignPath, Linux GPG), packaging manifests for
  Homebrew, AUR, winget, Chocolatey, Scoop, Snap, Flatpak, Nix, and
  cargo install.

### Known notes

- BGE-M3 dense/sparse use nearest fastembed equivalents until upstream
  ships native BGE-M3; swap is config-only.
- Apple notarization and SignPath signing activate once the maintainer
  completes the one-time enrollments (documented in
  .github/docs/code-signing.md); unsigned builds work everywhere today.
- The legacy shell proxy/dashboard remain in src/ until the Rust daemon
  has shipped one stable release (non-destructive sequencing policy).

## [0.1.0] - 2026-05-28

### Added

- Gemini proxy daemon (`src/bin/cascade-gemini-proxy`) running on `localhost:3761`, rotating across 28 Gemini API keys from vault, writing per-account utilization to `~/.claude/temp/quota-state.json`
- Fleet dashboard web UI (`src/web/`) on `localhost:9761`, reading `quota-state.json` and rendering per-account utilization
- `install.sh` and `uninstall.sh` for local setup
- Absorbed claw-fleet (Gemini proxy daemon) and claw-dash (dashboard web UI) into a single unified tool
- MIT license

[Unreleased]: https://github.com/acamarata/cascade/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/acamarata/cascade/releases/tag/v0.1.0
