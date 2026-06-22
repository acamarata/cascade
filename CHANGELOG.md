# Changelog

All notable changes to cascade are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)

## [Unreleased]

## [1.1.0] - 2026-06-22

Fleet + onboarding + self-contained hardening.

### Added

- Fleet poller — a daemon loop refreshes `~/.cascade/quota-store.json` every
  ~60s (configurable `[fleet]`) via a `FleetSource` trait. GFP source is live;
  Claude/Codex/Gemini sources are stubs (return no data, never faked) pending
  v1.2 model-access. The menu-bar tray shows a fleet usage readout.
- Onboarding wizard wired end-to-end — provider connect/add-GFP/list, wizard
  state persistence, filesystem/symlink/keychain commands, and automatic
  credential detection on first run (no more silently-failing steps).

### Changed

- Self-contained: removed all Docker/cross-rs reliance from CI and the release
  pipeline. Linux aarch64 now builds + tests natively on GitHub arm64 runners.
- `cascade import` discovery is scoped to instruction content — harness-runtime
  directories (session transcripts, caches) and non-instruction files are
  skipped, so importing a real `~/.claude` is fast and lossless.

### Fixed

- `project_poller` is wired behind the `gfp` feature (was an orphaned module).

## [1.0.0] - 2026-06-22

First stable release. Cascade is a standalone, local-first context manager and
Claude Code extension: one source of truth for AI instructions, knowledge, and
guardrails that every AI coding tool can read. DB-free (local SQLite/sqlite-vec
only), localhost daemon only, no server.

### Added (Parity Program P11-P14)

- Six-tier instruction cascade (GCI > PCI > APC > PPC > PRC > PAC) with a
  resolution engine and harness-native file generation (CLAUDE.md, AGENTS.md,
  .cursorrules, opencode) from a single CASCADE.md.
- `cascade import` — lossless migration of a hand-built `.claude`/`.opencode`/
  `.codex` setup, gated by a coverage ledger + deterministic round-trip proof.
- Canonical variables (`${ns.key}`) interpolated at resolve time.
- `cascade doctor` drift-linter — dangling pointers, hand-edited generated
  files, cross-tier conflicts, and an always-loaded context-budget lint.
- Five-channel RAG fusion (FTS5 + dense vector + curated + recency via RRF) with
  retriever-level scope exclusion for privacy.
- `provide_harness_context` MCP tool; MCP server for five client tools.
- `cascade verify` health gate; headless `cascade init --accept-defaults`.
- Cross-platform daemon install (macOS launchd, Linux systemd, Windows).
- Snapshot-before-regenerate + atomic/symlink-safe generation.

### Changed (lean v1.0 scope)

- GFP provisioning, gemini-proxy, and cascade-ccapi are feature-gated OFF by
  default — the default binary is lean and offline, with no external-network
  provisioning surface.

### Security

- wasmtime pinned at 36.0.11 (clears RUSTSEC-2026-0182); injection-guard hook;
  42-pattern destructive deny-list.

## [0.9.3] - 2026-06-16

Parity Program P13 — runtime integration + security hardening. Cascade can now
reach into the harness runtime and ships injection-aware guards.

### Added

- `cascade configure --harness claude-code` — idempotently writes a
  Cascade-managed block into the harness `settings.json` (an `env` block and a
  `permissions.deny` array derived from the policy engine's 42 deny patterns).
  Dry-run by default; `--apply` writes atomically; user keys are never touched
  (everything lives under a single `_cascade_managed` key).
- Prompt-injection scanner (`cascade_core::injection_scan`) — instruction-
  override, system-prompt-extraction, deny-list-override, jailbreak-framing, and
  encoded-payload detection with an ordered risk model and configurable
  sensitivity.
- `cascade check injection` + `scripts/hooks/injection-guard.sh` — a
  `UserPromptSubmit` hook that scans the prompt and gates on exit code (0 clean,
  1 warn, 2 halt) so injections are caught before tool dispatch. Contract in
  `.github/docs/injection-hook.md`.
- Agent prompt-size gate — agent system prompts are token-estimated at spawn;
  warn above 2000, error/refuse above 4000 (configurable), with the three
  largest sections reported.

### Changed (security)

- Destructive deny-list — full parity (10 → 42 patterns) with injection-resilient
  evaluation: base64/URL/unicode-homoglyph normalization + chained-command
  splitting. OWASP LLM Top-10 mapping in `.github/docs/deny-list-audit.md`.

### Verification

Built + tested on Linux (Docker `rust:1.96`) and native macOS; clippy
`-D warnings` clean. GitHub Actions quota was exhausted this cycle — CI will
re-confirm on reset.

## [0.9.2] - 2026-06-16

Parity Program P12 — content parity. Cascade now ships useful defaults and
richer instruction handling.

### Added

- Model behavioral-profile routing: the model-tier registry carries per-model
  profiles (default format, tool-use trigger, refusal sensitivity, best-for);
  agent spawns can route by profile match within a tier, falling back to pure
  tier resolution when no profiles are configured.
- `@import` expansion in instruction resolution — `@path` references are inlined
  at resolution time (relative to the tier's `.cascade/` dir) with nested
  imports, cycle detection, missing-file tolerance, and a depth cap.
- Default behavioral-rule library — six generic shippable guardrails
  (destructive-action deny-list, autonomous-verification, output-conciseness,
  excellence-in-engineering, anti-drift, version-lock) as `tier = "any"`
  templates.
- Per-language coding-standard templates — TypeScript, Rust, Python, Go (stack
  targeted) plus universal Git and Security standards.
- Cross-tier no-duplicate lint via `cascade doctor` — flags when a lower tier
  repeats a higher tier's content verbatim.
- All shipped content is fresh and fully generic (no personal/infra detail).

### Fixed

- `cascade verify` tests that assumed local-model availability are now gated
  behind the opt-in `local-llm` feature; the empty-dir resolution test is
  hermetic (isolates `HOME`).

### CI

- Distribution publish/update workflows now skip cleanly when their secret is
  absent instead of failing.

### Security

- Bumped wasmtime + wasmtime-wasi 36.0.10 → 36.0.11 to clear RUSTSEC-2026-0182
  (GHSA-3p27-qvp9-27qf), staying within the 36.x LTS line.

### Verification

Built + tested on Linux (Docker `rust:1.96`) and native macOS; clippy
`-D warnings` clean. GitHub Actions quota was exhausted this cycle — CI will
re-confirm on reset.

## [0.9.1] - 2026-06-15

Parity Program P11 — foundational correctness + cross-platform build hardening.
First increment toward Cascade fully replacing a hand-built multi-harness
AI-coding setup. Linux/macOS verified locally via Docker + native (GitHub Actions
quota was exhausted this cycle; CI will re-confirm on reset).

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
- `cascade-local-llm` (candle/gemma on-device inference) is now an opt-in
  feature (`--features local-llm`). The default daemon/CLI build no longer pulls
  the candle/gemm ML stack, so it builds cleanly on Linux. RAG embeddings are
  unaffected (they use fastembed/ONNX, not candle).

### Fixed

- **Linux Secret Service keychain** rewritten against oo7's real async API — it
  previously used `oo7::blocking`, which exists in no oo7 version, so the Linux
  backend had never compiled.
- **Windows keychain**: `CredEnumerateW` flags now use the `CRED_ENUMERATE_FLAGS`
  newtype required by `windows-0.58` (was a type error that never compiled).
- Platform-conditional clippy lints across the daemon-side crates; the full Linux
  daemon/CLI tree now builds and passes `clippy -D warnings`.
- cargo-deny bans: pinned internal path-dependency versions on all plugin and
  example crates (resolves the v0.9.0 release pipeline's cargo-deny failure).
- CI pipeline repairs: cargo-deny direct invocation, pnpm setup ordering + a root
  `package.json`, qmllint flag, and a darwin-scoped CC override (a global one had
  broken the Windows MSVC build).

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
