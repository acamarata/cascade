# Changelog

All notable changes to cascade are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)

## [Unreleased]

## [1.9.0] - 2026-06-27

Personal OS, three-mode chat, and release-readiness. Cascade is now a complete local-first personal + dev operating system: an encrypted personal data store, threads/topics, a namespace-isolated memory engine with three-mode chat, external-session harvesting, and the security/privacy/docs gates for a public release.

### Added — Personal OS & memory
- **Encrypted personal data store** (`rag-16`): new `cascade-personal` crate — AES-256-GCM at rest (key in the OS keychain), seeded + custom collections, a mode-aware gate (finance/health/credentials hidden outside Personal mode), and a consent/exposure log.
- **Threads / topics / archive** (`rag-15`): markdown↔DB synced personal threads with stage tasks, topics, cross-thread search, and non-destructive archiving.
- **Memory engine + three-mode isolation** (`rag-08`): `memory_episodes`/`memory_facts`/`chat_history` with BLAKE3 dedup, consolidation + decay, and strict namespace isolation (`personal` / `dev-<project>` / `meta`). The personal namespace is firewalled at both the recall layer and the MCP tool boundary. New `remember`/`recall`/`forget`/`search_memory` MCP tools.
- **CC session harvest** (`mem-01`): a `POST /api/harvest/cc-session` endpoint + an idempotent Claude Code `Stop` hook extract decisions/file-changes/tool-patterns into the project's `dev-<slug>` memory (personal namespaces never harvested without opt-in).

### Added — App & retrieval
- **Three-mode chat + navigator** (`app-01`): Personal / Projects / Cascade scope switcher (`?scope=` URL state), DB-backed chat history, top-level Personal + Projects routes, and a remapped sidebar.
- **Fleet widget UI** (`fleet-02`): the fleet widget mounts in the status bar with a unified usage panel and editable quota estimates.
- **Caching, privacy & multi-tenant** (`rag-07`): exclusion-set enforcement in search + ingest, tenant/project-scoped cache invalidation, embed-cache LRU+TTL, and a secret/PII redaction pipeline.
- **Roadmap retrieval** (`rag-09`): multi-query + step-back expansion, code-graph structural queries, a bounded agentic retrieve loop, and a feedback-signal ingest point.

### Added — Release gates & ops
- **MCP transport auth** (`sec-01`): runtime loopback enforcement in all build profiles, Origin/Host (DNS-rebind) middleware, capability-scoped HMAC tokens gating personal-data tools, and an access audit log.
- **Telemetry opt-in** (`priv-01`): off by default (config-gated), first-run consent (defaults No), `cascade telemetry` CLI, and `PRIVACY.md`.
- **Plugin security** (`plug-01`): PersonalData/McpInvoke capabilities, a grants store, Ed25519 signing with a trusted-publisher registry, and an audit log.
- **Backup / export** (`data-01`): `cascade export`/`import` portable archives with BLAKE3-verified manifests and secret exclusion.
- **Scale benchmarks** (`perf-01`): 100k/1M sharded-search + fleet-router benches with absolute thresholds and a nightly gate.
- **FOSS docs** (`doc-01`): rewritten README/CONTRIBUTING/Quickstart/Configuration, consolidated wiki, and a static landing page.

### Fixed
- Deterministic chat-history ordering (`created_at, id`) on same-timestamp inserts.
- Scrubbed remaining dev-machine paths from tests/docs (FOSS CI guard green).

## [1.8.0] - 2026-06-27

Zero-config + PEWS + intelligence. Cascade now discovers and indexes your projects automatically, ships a tiered agent roster with a soul/verbosity layer, and drives autonomous phase builds.

### Added — Zero-config / "it just works"
- **Configurable roots** (`rag-12`): `personal_dir` + `projects_dirs` config with `effective_*` helpers; tier paths de-hardcoded (`$CASCADE_APC_PATH` still wins).
- **Project discovery + registry** (`rag-13`): `ProjectType`/`ProjectRecord` taxonomy, marker-file classifier, `registry.db`, two-pass scanner with nested-root dedup (inner wins) and monorepo sub-app detection.
- **Zero-config activation** (`rag-11`): `rag.enabled`/`mcp.enabled` default **true**; the supervisor now spawns the previously-dead `AutoRagWatcher` + `IndexingPipeline` + `VolumeIndexGuard` (plus the `auto-01` Scheduler) and runs a bootstrap project scan; `cascade wizard` first-run setup; watched formats add txt/pdf/docx/xlsx.
- **MCP self-registration** (`frame-01`): idempotent merge of `mcpServers.cascade` into `~/.claude/settings.json` (preserves other servers); PEWS + Personal skill suites; `--system` profiles; agent-TOML install to `~/.cascade/agents/`.

### Added — Intelligence & runtime
- **Background automation** (`auto-01`): the Scheduler is finally spawned; `HookEvent::TurnComplete`; `BackgroundTaskClass` + capability gate; AutomationRunner + sample automations.
- **Agent roster** (`agents-01`): tiered roles + 14 default agent TOMLs referencing souls + model tiers; override-merging registry; role→tier table.
- **Soul layer** (`soul-01`): per-agent personality + verbosity 1–10 (default 3); `resolve_soul` compositor.
- **Context assembler** (`ctx-01`): minimal-by-default per-model context assembly; E-05 retrieval stub unblocked; 5 role profiles.
- **Codebase study** (`rag-14`): code-graph adjacency, tech-stack detection, template OVERVIEW with BLAKE3 cache (RAPTOR/arch tracked).

### Added — PEWS & retrieval
- **Phase lifecycle** (`pews-01`): Opening/Wrapup statuses + readiness gate (old values via serde aliases).
- **Autonomous build engine** (`pews-02`): `BuildEngine` topo-walks the ticket tree, runs EOSt + EOx gates; `cascade build run`.
- **Phase UI** (`pews-03`): all-projects board with lifecycle pills, project selector, build-progress panel.
- **Fusion + eval** (`rag-05`): config-weighted RRF, norm strategies, query routing, MAP@K + golden-fixture regression.
- **Chunking** (`rag-06`): unified `ChunkConfig`; ingest dedup switched to BLAKE3 (rag-10 #8).

### Fixed
- `cascade.search` returned no citations when chunk_ids were non-numeric (RrfRetriever now falls back to raw hits).
- Hermetic router/env tests (no longer read the developer's real `~/.cascade`).
- Scrubbed dev-machine paths from `config.rs` (FOSS CI guard green).

## [1.7.0] - 2026-06-27

RAG core + FOSS gate. Real local embeddings + reranking, a provider-agnostic Fleet router with no shipped accounts, vector-index correctness fixes, and migration safety.

### Added
- **Local embeddings** (`rag-02`): real ONNX inference via fastembed 4.9.1. Multilingual dense embeddings (MultilingualE5Large, 1024-d, 100+ languages), `query:`/`passage:` instruction prefixes, a BLAKE3 content-hash embed cache, and Matryoshka `truncate_dim`. (fastembed 4.9.1 ships no BGE-M3/SPLADE/ColBERT; tracked as TODO for direct-`ort` tri-mode.)
- **Cross-encoder reranking** (`rag-03`): `BGERerankerV2M3` wired into the live search path (previously the reranker was constructed but never passed — it never ran in production). Candidate pool `k × multiplier` (default 20), sigmoid-scored, sorted by reranker score.
- **Functional dense retrieval** (`rag-01`): `VectorRetriever` now returns real KNN hits (`vec0` under the `vec` feature, squared-L2 fallback otherwise) instead of an empty stub.
- **FOSS Fleet router** (`fleet-01`): routing derives entirely from the live account registry by capability/role/tier — no hardcoded accounts. Empty registry returns a typed "setup required" with zero panics.
- **Migration safety** (`mig-01`): `schema_version` validation on runtime YAML (typed future-version rejection) and backup-before-migrate on the tasks DB.

### Changed
- **FOSS genericization** (`frame-02`, release gate): the default account registry ships **empty**; dev-machine absolute paths scrubbed from all tracked files; new `scripts/check-no-maintainer-ids.sh` CI guard; `AUTHORS` added; `*.pem`/`*.key`/`*.secret` git-ignored.
- Upgraded fastembed 3 → 4.9.1, repairing the `ort-sys` build break so the ONNX path compiles.

### Fixed
- rag-10 `#1` (embedder offline guard now fires before `create_dir_all`), `#4` (ColBERT no longer emits a silently-wrong shape — clean error + TODO).
- rag-04: `#5` sharded eviction no longer orphans vectors, `#2` sharded health count sums across shards, `#6` legacy migration rebalances `shard_0` across shards instead of dumping everything into it.
- Deleted the dead `JinaReranker` stub.

## [1.6.0] - 2026-06-27

Foundation: embedded data layer. First milestone of the local-first personal+dev OS buildout — the shared SQLite substrate every later subsystem (RAG, registry, jobs, personal store) builds on.

### Added
- New `cascade-db` crate — the single foundation for all SQLite access:
  - `configure_connection` / `open_configured`: canonical PRAGMA set (WAL, `busy_timeout=5000`, `synchronous=NORMAL`, `foreign_keys=ON`, 64 MiB cache, `temp_store=MEMORY`).
  - Versioned migration runner (`MigrationRegistry`) with backup-before-migrate and typed future-schema rejection — the home of the migration framework.
  - Embedded cache (`CacheBackend` + `SqliteCache`, optional in-process `moka` cache).
  - Durable SQLite job queue (`JobQueue` + `SqliteJobQueue`) with claim-lease semantics.
  - ANN vector store (`VectorStore` + `SqliteVecStore`, sqlite-vec/cosine), default-on.
  - Canonical BLAKE3 content hashing (`content_hash`).
  - Redis is never required — available only behind the `redis-backend` feature.

### Changed
- All 16 SQLite connection sites across `cascade-core`, `cascade-daemon`, `cascade-rag`, and `cascade-cli` now open through `cascade_db::open_configured`. Critically, `busy_timeout` — previously unset on every database, a latent `SQLITE_BUSY` race — is now applied everywhere, along with consistent foreign-key and cache settings.

### Fixed
- rag: sparse (BM25/FTS5) scores are now carried into citations instead of being silently dropped (`rag-10` #3).
- rag: removed an unnecessary double allocation per embedding call (`rag-10` #9).
- rag: removed a dead similarity-threshold field from `SemanticChunker` (`rag-10` #7).

## [1.5.0] - 2026-06-23

Accounts subsystem + native fleet widget.

### Added

- Account registry directory at `~/.cascade/accounts/` (`accounts.json` +
  `quota.json` + `README.md` + `matrix.md`) — tracks every account (Claude
  primary + pooled, Codex, Gemini, OpenCode-Go, the GFP free-Flash key pool)
  with its access method (native Claude Code, smithers/claude-p, codex CLI, agy
  CLI, opencode CLI, GFP key pool), available models, detected CLI availability,
  GFP key count, and quota links. The daemon refreshes `quota.json` every tick
  (~60s) for the widget to read.
- `cascade accounts list | status | matrix | detect` — view and refresh the
  registry and the model best-for matrix. Keys are counted, never logged.
- Model routing matrix + research (`.github/docs/MODEL-ROUTING.md`,
  `data/model-matrix.json`) — which model is best for which task and how it is
  accessed, with an account-exhaustion strategy that reserves the primary
  Claude session and drains pooled accounts and the free GFP pool first.
- Native macOS fleet widget (`src/widget/macos/CascadeApp`) — both an always-on-
  desktop panel (the Claw-Fleet replacement) and a menu-bar `NSStatusItem` +
  popover, showing every account with its 5-hour and weekly quota windows and
  reset countdowns, reading `~/.cascade/accounts/quota.json` and refreshing every
  30s. Left-click the menu-bar icon toggles the desktop panel; right-click opens
  the popover. Installs a LaunchAgent so it starts at login. Replaces the previous
  Übersicht-based widget.

### Fixed

- Widget never appeared: a SwiftUI `App` with only a `Settings` scene under
  `LSUIElement` did not reliably fire `applicationDidFinishLaunching`, so no UI
  was created. Switched to a classic AppKit `NSApplication.run()` entry point, and
  anchored the desktop panel to the primary screen (a secondary display offset the
  position off-screen).

- Privacy: removed hardcoded developer home path and a personal project list
  from the shipped desktop app (home dir is now resolved at runtime); removed
  a private email address from all public artifacts (contact fields, packaging
  metadata, release runbook).
- Model IDs corrected throughout to the current lineup (`claude-opus-4-8`,
  `gemini-3.5-flash`) and routed through the canonical model registry to stop
  drift.

## [1.4.0] - 2026-06-22

Baked-in behavioral instruction layer.

### Added

- Default behavioral rules shipped with Cascade and generated into every
  harness's files: authorization & autonomy (act-then-report within standing
  authorization, configurable posture), vision & mission discipline (stay in
  scope, flag missing specs, no gold-plating), and delegation & model
  discipline (route per the model matrix, max free quotas for review). These
  join the existing anti-drift, dynamic-learning, and excellence rules.
- `cascade doctor` behavioral-core check — flags drift when a generated harness
  file is missing the always-loaded behavioral rules (e.g. hand-stripped).

## [1.3.0] - 2026-06-22

Dynamic learning + taxonomy.

### Added

- `cascade memory capture "<text>"` — classifies a note (decision / lesson /
  pattern + domain tags), routes it to the right memory file with a timestamp
  and tags, and makes it retrievable. Captures learnings as you work.
- Taxonomy auto-classification — rule-based by default, optionally GFP-assisted
  via the free-Flash routing lane, with graceful fallback when no provider is
  available.
- `cascade search --scope memory` — semantic/keyword search scoped to the memory
  corpus.
- A shipped `dynamic-learning` behavioral rule (tier "any") that makes capturing
  decisions/lessons/patterns part of an agent's definition of done.

## [1.2.0] - 2026-06-22

Model access + privacy firewall.

### Added

- Sensitive-data firewall — a content classifier (PII, VA/disability,
  custody/family, health, financial, personal-scope paths) plus a dispatch
  guard that keeps sensitive content on Claude or local models only. It is
  provably blocked from every external provider (Gemini, OpenAI/Codex, GFP,
  OpenCode-Go) and never synced.
- Local delegate-out lanes — `cascade dispatch` can route work to `codex exec`,
  `agy -p`, `opencode run`, the GFP free-Flash pool, extra Claude accounts
  (`claude -p`), and a local LLM. Each lane detects CLI availability and never
  fabricates output.
- Quota-aware routing matrix — `cascade dispatch --route <class>` selects a lane
  by task class and live quota headroom: the primary Claude session stays
  reserved for interactive use, extra accounts drain first, cheap work prefers
  the free GFP pool, adversarial review prefers a different model family, and
  sensitive work is firewalled to Claude/local. Paid-API overage is off by
  default.

### Changed

- Mac tier defaults — `~/Sites` resolves to the project (APC) tier and
  `~/Downloads` is treated as the personal scope (locked + firewalled) with no
  configuration required.

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
