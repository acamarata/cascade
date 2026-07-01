# Changelog

All notable changes to cascade are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)

## [Unreleased]

## [1.9.18] - 2026-07-01

Chat modes made real, honest GP pool count, and a broad app fix pass.

### Fixed
- **Cascade chat scope existed in the store but not the UI** — the mode switcher only offered Personal/Projects. All three modes now render and map to real memory namespaces (`personal` / `meta` / `dev-<project>`).
- **Chat history persistence had never worked** — the app sent `namespace` without the required `scope` (400) and hit the personal-namespace firewall without `opt_in` (403), silently falling back to localStorage. Correct scope, opt-in, and namespace mapping now match the daemon's validator, so chats persist server-side.
- **Personal page called the wrong endpoints** (`/api/memory/personal/threads` vs the real `/api/personal/threads`, wrong response shape) — it always failed; the thread detail panel was a stub. Both now use the real endpoints.
- **Collapsed sidebar caused full page reloads** (bare `<a>` instead of router links), dropping all app state; dead routes and stale labels (`/vault/graph`, "Dashboard") cleaned up; the Projects board "Plan" button actually seeds the chat now.
- **Explicit provider choice is respected in chat** — picking a provider no longer routes through the Gemini-pool fast path first.
- **Widget quota gauges knew nothing about current model ids** (showed "?" for all live usage) — model map updated for the current fleet including `claude-sonnet-5`.
- **GP pool key count is honest** — the pool size shown in the widget counted vault lines (duplicates across two vault files, placeholders included). It now counts unique valid keys only.

### Added
- **Private chat inside Personal mode** — a per-session private toggle whose messages never reach the daemon (local-only, cleared on reload).
- **`context.personalRoot` setting** — Personal mode's file scope is configurable (defaults to the user's downloads workspace).

## [1.9.17] - 2026-07-01

### Fixed
- **`cascade conductor selftest` bounds each provider probe** (30s) so a hanging or slow backend (e.g. opencode waiting on a rate-limited upstream) can no longer block the whole run — every provider is now reported (ok / FAILED / skipped / unavailable) instead of the run timing out partway.

## [1.9.16] - 2026-07-01

Cascade Conductor — quota-aware multi-account/multi-provider routing — plus the Sonnet 5 model bump.

### Added
- **Cascade Conductor** (`cascade conductor`): the primary Claude Code session (A1, T0) stays interactive on its own account while delegated work is routed to the best available backend, matching each task to the model best at it and spilling by live quota. Worker spill order: **A2 → A1 spare → Codex → Gemini → OC Go → GP** (skipping any account that is auth-dead or at its 5h/7d cap). Model class per tier (T1→Opus, T2→Sonnet, T3→Haiku; Fable when available), mapped to concrete model ids.
  - `cascade-core/conductor_router.rs` — pure, unit-tested selection (`select_target`) reading live `quota.json`.
  - `cascade-cli` `cascade conductor --tier <T1|T2|T3> [--model …] [--account …] --prompt … [--dry-run]` + `conductor selftest` (live per-provider probe: available/unavailable + latency, so no adapter can be a silent stub).
  - Real executor adapters for Claude (A1/A2 via `claude -p`), Codex, OC Go, Gemini, and GP; on backend failure it falls to the next target and never fabricates success.

### Changed
- **Sonnet 5** is now the canonical Sonnet model (`claude-sonnet-5`), used by the harness default and Conductor T2 routing. Added its pricing entry.

## [1.9.15] - 2026-07-01

Daemon-owned nSentry local sync — Cascade fully owns the developer-machine side of the observability pipeline.

### Added
- **`cascade nsentry` — declarative, daemon-run report sync for every project.** One config (`~/.cascade/nsentry-sync.yaml`) lists each project (path, GitHub org, sentry box, inbox) and its three streams; the daemon schedules them, no launchd or hand-made scripts:
  - **rsync** (~5 min): box `/opt/nself-ops/errors/*.md` → inbox, per-dev `consumed.list` dedup (reuses the `cascade sentry` engine).
  - **ci** (~15 min): the org's GitHub Actions failures → Markdown → inbox, deduped via `.gh-seen`.
  - **dependabot** (~6 h): org Dependabot alerts + version-update PRs → Markdown → inbox, deduped via `.dependabot-seen`.
  - Bundled bridge scripts (`crates/cascade-daemon/assets/nsentry/`) are materialized to `~/.cascade/nsentry/scripts/` at start and invoked per project; `gh`/`rsync`/`bash` resolved by absolute path.
  - **`per_run_cap`** bounds reports delivered per run so a CI "fixing storm" or large backlog can't flood an inbox.
  - **`cascade nsentry status`** shows per-project/stream last-run, delivered (last/total), and error — a stalled sync is obvious; state persists to `~/.cascade/nsentry-sync-status.json`. Plus `list`, `run`, `pause`, `resume`.
  - **Safety**: consumed reports never re-deliver; writes only inside the configured inbox; an unreachable box (or changed SSH host key) logs + records the error and continues without affecting other projects.
- Docs: `docs/nsentry-local-sync.md` (how it works, schema, adding a project, reading status, safety).

Verified end to end against four live sentry boxes (nself, unity, ummat, acamarata): a sentinel delivered exactly once to the correct inbox, dedup on re-run, and zero cross-inbox leakage.

Live Claude usage in the daemon, and honest multi-account auth state.

### Added
- **The daemon now fetches live Claude Max usage itself.** Previously `ClaudeMaxSource` was a stub returning `None`, so the widget/app could only show usage second-hand from an external poller — and it went stale. A new `cascade-daemon/claude_usage` module calls `GET api.anthropic.com/api/oauth/usage` for each discovered Claude account (10s timeout, ISO-8601 `resets_at` → epoch, error-envelope aware), refreshing an expired token via a bounded headless `claude` invocation (MCP/plugins skipped so heavy configs don't hang). `external_accounts::read_claude_access_token` exposes the live keychain token for the fetch.

### Fixed
- **Per-account usage isolation.** `find_legacy_entry`'s provider-level fallback no longer assigns one Claude account's usage to another when several share a provider — it requires an exact id match when 2+ same-provider entries exist. A credential-dead account shows dashes, not a sibling's numbers.
- **Auth state reflects ground truth.** A real API 401 (after a `claude` refresh attempt) now flags an account for re-auth even when a refresh-token string is still present in the keychain — the live fetch is authoritative over the optimistic keychain heuristic. Transient errors (network, rate-limit, parse) still never trigger a false re-auth nag. The daemon resolves the `claude` binary by absolute path so this works under launchd's minimal PATH.

nSentry per-project state isolation, plus FOSS cleanup of the Forgejo CI mirror.

### Fixed
- **nSentry state is now keyed by both developer and project**, not developer alone. The sync cache and `consumed.list` manifest moved from `~/.cascade/nsentry/<dev_id>/` to `~/.cascade/nsentry/<dev_id>/<project>/`. Without this, two projects synced on one machine shared a single cache (rsync runs without `--delete`) and a single manifest, so reports from one project's server could be copied into another project's inbox and dedup decisions collided across projects. Each project now keeps a fully independent cache and manifest — verified with four projects syncing into separate inboxes with zero cross-contamination. The launchd label slug and the state-directory slug are derived from one shared `project_slug` so they stay in lockstep.
- **Forgejo CI mirror genericized for FOSS.** `.forgejo/workflows/ci.yml` no longer hardcodes any maintainer host or path — the failure-reporting hook now reads `NSENTRY_SERVER` from the operator's Forgejo variables and writes to the repo's own `.claude/inbox`. `scripts/check-no-maintainer-ids.sh` was hardened to scan `.forgejo/` and reject maintainer domains, so this can't regress.

## [1.9.12] - 2026-06-30

nSentry report sync — pull bug/CI/error reports from a project's ops server into its Claude Code inbox, deduplicated per developer.

### Added
- **`cascade sentry` — nSentry bug/CI/error-report sync.** A project's monitoring server writes timestamped Markdown reports to a remote directory (default `/opt/nself-ops/errors`); Cascade pulls them into that project's `.claude/inbox` so the local AI can act on them like any other inbox item.
  - **Sync engine** (`cascade-core/nsentry.rs`): `rsync -az` over SSH from `<server>:<remote_dir>/*.md` into a local cache, then copies each report **not yet in a per-developer `consumed.list` manifest** into the inbox and records it. Idempotent and multi-dev-safe — every developer sharing one server receives each report exactly once; re-runs copy nothing. rsync against an unreachable host returns a typed error, never panics.
  - **Per-developer identity**: a stable 12-char `dev_id` derived locally from hostname + username (nothing leaves the machine); state lives in `~/.cascade/nsentry/<dev_id>/`, out of the project tree.
  - **Per-project config** `<project>/.cascade/nsentry.toml` (`sentry_server`, `remote_dir`, optional `inbox`, `interval_secs`). No server address is hardcoded in Cascade — all values are user-supplied.
  - **Commands**: `enable` (writes config + installs a macOS launchd agent that syncs on an interval and at login), `sync` (`--dry-run` supported), `status`, `disable`, `update` (regenerate the agent after a binary or interval change). On Linux the config is written and the sync command can be wired to a systemd timer or cron.
  - Docs: `.github/wiki/nSentry.md`. Tests cover dev_id stability, config round-trip, and the rsync→copy→dedup→isolation flow.

## [1.9.11] - 2026-06-30

Fixes the widget's persistent "click here to re-auth" on accounts that are authenticated fine in the desktop apps.

### Fixed
- The fleet poller (`src/bin/cascade`) refreshed expired Claude Max tokens with a direct OAuth `POST` to `platform.claude.com`, which Cloudflare bot-protection rejects (HTTP 403 "1010"). Automatic refresh silently failed, so the widget flagged "re-auth" for accounts that were perfectly usable. `refresh_token()` now refreshes **through Claude Code itself** (`CLAUDE_CONFIG_DIR=<dir> claude -p`), which works headlessly, bounded by a 45s timeout so a non-TTY (LaunchAgent) hang can't stall the poller. Only a genuinely revoked refresh token now reports `refresh_failed` (that account needs one interactive login).
- Together with the v1.9.10 daemon bridge (which derives the widget's auth status from the live keychain token, marking an account "ok" when it has a valid/refreshable token), an account that works in Claude.app/Claude2.app now shows "ok" in the Cascade widget.

## [1.9.10] - 2026-06-29

External-account credential bridge — fixes the repeated re-auth prompts in the Cascade app and widget.

### Fixed
- The app/widget no longer nags for re-auth on Anthropic (or Codex) accounts that are already authenticated in the desktop Claude apps. Cascade was holding its own credential copy that never refreshed; it now reads each external agent CLI's **live, app-maintained** token directly.

### Added
- `cascade-core/external_accounts.rs` — discovers and reads external agent accounts:
  - **Claude**: `~/.claude` (primary), `~/.claude2…N`, and legacy `~/.claude-accN`. macOS reads the login-keychain entry the desktop app keeps fresh (`Claude Code-credentials-<sha256(dir)[:8]>`, via the `security` CLI; token never logged); other platforms read `<dir>/.credentials.json`. A token counts as authenticated when the access token is present and either unexpired or backed by a refresh token.
  - **Codex**: `~/.codex` / `~/.codex2…N` via `<dir>/auth.json`.
  - **Roles**: `~/.claude` is **Primary** — the surface Cascade enhances (skills/MCP/rules/proxy) and the orchestrator that launches delegated work; every other account is **Pool** (delegation workers).
- Account auth status (`quota.json` → widget) is now derived from the live bridge: a valid live token shows "ok" even if the legacy poller reported a refresh failure; each account entry records its `config_dir`, and the widget's re-auth action is scoped to that account's `CLAUDE_CONFIG_DIR`.

`~/.cascade` remains Cascade's own state directory; the bridge only reads external dirs.

## [1.9.9] - 2026-06-29

Remediation patch — the last completable deferred items.

### Added
- **Encrypted personal vault in the desktop app** (`rag-16-ui`): the `cascade-personal` vault was fully implemented but unreachable from the GUI. Six Tauri commands (open / list-collections / query-records / upsert-record / request-consent / exposure-log) now bridge it, each opening the vault fresh via the OS keychain; a Personal → Vault tab provides the UI (mode toggle, sensitivity-badged collections, records table + add form, exposure log).
- **Live Fleet routing stream** (`fleet-01-events`): a decoupled `RoutingObserver` seam on the core router (default no-op, no daemon coupling) feeds a 64-event ring exposed at `GET /api/fleet/routing`; `FleetRoutingView` now shows a live task→account/model/reason table alongside the existing quota view.

### Changed
- **rag-04**: shard rebalance processes very large legacy indexes in 1000-row batches (bounded memory) instead of a single pass.
- **rag-14**: project-overview synthesis optionally enriches via an injected LLM, falling back to the template (the default working path) on absence/error.

### Known roadmap (working implementations in place; not stubs)
- `rag-02` true BGE-M3 SPLADE/ColBERT needs direct-`ort` multi-output (dense + TF-IDF sparse work today); `rag-09` LoRA feedback *training* (signal collection works); `rag-06` rayon ingest (sequential path works); `pews-02` fully-autonomous build dispatcher (`cascade build run` uses a labeled dry-run dispatcher). Each is tracked in-code.

## [1.9.8] - 2026-06-28

Security layer — the Cascade way. App-shipping security checks (secret leaks, dependency CVEs, client-side key exposure, error-message leakage) integrated into Cascade's existing systems rather than a bolted-on always-on scanner: a tiny always-loaded behavioral rule, a triggered hook, deferred MCP tools, user-pulled skills, and a spawnable agent. Zero overhead on a normal session; full coverage only when triggered.

### Added
- **`cascade-security` crate** — the shared scanning core: regex secret detection (private keys, AWS/Google/GitHub/Slack/Stripe tokens, generic key assignments) with redacted previews and placeholder filtering; client-side-leak classification (a secret in `public/`/`static/`/frontend bundles is high-severity); multi-ecosystem dependency audit (`cargo`/`npm`/`pnpm`/`pip audit`, graceful when the tool is absent); error-message-leak heuristics; `prelaunch_scan`.
- **`cascade security` CLI** — `scan-file`, `secret-scan`, `audit`, `prelaunch`, `scan-hook` (`--json`). `scan-file` exits non-zero on a client-side secret so a hook can block.
- **Always-loaded behavioral rule** (4 lines) — generated into every harness's `CLAUDE.md`/`AGENTS.md` and enforced by `cascade doctor`: no client-side secrets, validate server-side, generic user errors, rate-limit paid-API endpoints.
- **Triggered PostToolUse hook** — Cascade self-registers a `Write|Edit` hook running `cascade security scan-hook` on the written file; fires only on writes, exits 0 silently if the binary/file is absent (never breaks a session).
- **Deferred MCP tools** — `cascade.security.secret_scan` and `cascade.security.audit` (schema-only until invoked).
- **Skills** — `/security-audit`, `/prelaunch`, `/rls-check` (Supabase RLS via the user's MCP if connected), `/deps-audit`, shipped as a universal Security suite installed alongside the chosen system suite.
- **`security-reviewer` agent** — spawnable for OWASP / deep review.
- **Opt-in EOx check** — `SecurityChecks` + a `WithSecurity<C>` wrapper that fails a phase gate only on high-severity findings (client-side secret or critical CVE); not forced into the default end-of-ticket flow.

## [1.9.7] - 2026-06-28

Remediation closeout: hygiene + the last stubs the verification re-audit surfaced.

### Fixed
- **CEO runtime uses the real provider.** The CEO orchestrator still ran on `NopRouter`/`NopInvoker` ("nop:" output); `CeoRuntime::with_registry` now wires the real `RegistryRouter` + `SafeToolInvoker` (provider registry threaded `main → IpcServer`). The no-registry fallback uses honest "no provider configured" / "not yet implemented" messages, never "nop:".
- **Reranker offline-guard works.** `create_dir_all` ran before the guard check, so it never fired; with the `reranker` feature enabled (via workspace feature-unification) this also panicked a test on `block_in_place`. The guard now runs first — absent model + offline_guard fails fast with no disk write or download.

### Changed
- **dead_code hygiene.** Removed the blanket crate-wide `#![allow(dead_code)]` from `cascade-daemon` and `cascade-pdk`; replaced with item-scoped `#[allow(dead_code)]` (each justified) so the compiler gives real dead-code signal.

### Verification
Full workspace: `cargo test --workspace --lib` — **3,114 passed, 0 failed**, deterministic. `cargo build --workspace` green; FOSS maintainer-id guard green; CLI↔daemon integration 11/11.

### Genuine future work (documented, not misleading stubs)
- SPLADE/ColBERT via direct-`ort` multi-output (sparse retrieval works today via BM25/TF-IDF).
- LoRA feedback adapter (signals are collected; training is future research).

## [1.9.6] - 2026-06-28

Remediation patch: the last two execution stubs.

### Fixed
- **MCP `sampling/createMessage` is real.** It returned a hardcoded `"[sampling not yet wired]"` string; it now delegates to the provider-backed sampling handler (the daemon injects the live provider registry). No provider → typed "no AI provider configured" error, never a fake success.
- **Plugin WASM ABI executes.** Plugin tool calls returned an empty `{}` instead of running the module; the real `cascade_plugin_call(ptr,len)->i64` dispatch is implemented (input written to guest memory, output read back), with `log`/`kv-get`/`kv-set` host functions and a per-invocation KV store. The PDK's host-import signatures were corrected to match. WAT round-trip tests prove real output.

### Genuine future work (not misleading stubs — documented)
- **SPLADE / ColBERT** dense-sparse-multivector via direct `ort` multi-output: fastembed lacks BGE-M3's sparse/colbert heads. The shipped sparse retrieval path uses BM25/TF-IDF and works today; SPLADE/ColBERT are a quality upgrade requiring a custom ONNX session + model files.
- **LoRA feedback adapter**: retrieval feedback signals are collected and stored; training a ranking adapter from them is future research. Marked `TODO(rag-09)`.

## [1.9.5] - 2026-06-28

Remediation patch (P2): the last LLM-orchestration + study stubs.

### Fixed
- **Board debate is real.** `board_debate` returned hardcoded "pending" stances; it now asks each board role (CEO/CTO/Architect, with its persona) for a real opinion via the provider, classifies stance, and computes consensus. No provider → explicit error stances, never fake.
- **RAPTOR + architecture extraction are real.** `build_raptor_tree` was a no-op (empty tree); it now does dir-locality clustering + per-cluster summaries (LLM when available, real extractive template otherwise), BLAKE3-cached. `extract_arch` emits a real Mermaid diagram from the code-graph adjacency (was an empty string).
- **`live_cc` PTY driver is real.** `LiveCcDriver::send_prompt` was a deferred stub that always errored; it now spawns the CLI in a PTY (`portable-pty`), captures output with a timeout, and maps binary-not-found/timeout to typed errors.

### Known remaining
MCP `sampling` transport and the plugin WASM ABI dispatch remain stubbed; true SPLADE/ColBERT (direct-`ort` multi-output) and the LoRA feedback adapter are genuine future work (the shipped sparse path is BM25/TF-IDF and works). Addressed/Documented in v1.9.6.

## [1.9.4] - 2026-06-28

Remediation patch (P1 part 3): the provider-dependent features are now wired to the real provider path.

### Fixed
- **HyDE is real.** Query expansion returned the query unchanged; it now asks the registered provider for a hypothetical passage and embeds that for the dense channel. No-provider/error falls back to the raw query (no regression).
- **Automation router is real.** The `AutomationRunner` used `NopRouter`/`NopInvoker` ("nop" output); it now routes each step through the provider registry (real completion). Tool execution returns an explicit "not yet implemented" the model can read instead of a fake success; an empty/unhealthy registry fails with a typed "no provider available" rather than fake success.
- **`cascade harness status`/`detect` are real.** Were hardcoded `false`/`[]`; now run real harness detection (installed state + binary path / JSON).

### Known remaining
Board-debate (agent orchestration), MCP `sampling` transport, real SPLADE/ColBERT and RAPTOR/arch summarisation, `live_cc` PTY, plugin WASM ABI, and the LoRA feedback adapter remain — tracked in-code and addressed in following patches.

## [1.9.3] - 2026-06-28

Remediation patch (P1 part 2 + P3 hardening).

### Fixed
- **Real RAG status.** `rag_status` reported all zeros; it now reads the real index — document count + `serving` from the index DB, `last_indexed` from `MAX(indexed_at)`, and `index_size_bytes` from disk. Zeros only when no index exists yet.
- **Personal threads reach RAG.** `push_to_rag` was a no-op; it now indexes each non-`locked` thread's title/README/open-task summaries into the `personal` memory namespace via a `RagSink` (the daemon injects a `cascade-rag`-backed sink; `locked` threads are skipped). New `POST /api/personal/threads/push-to-rag`.
- **Poison-tolerant locks.** Six `Mutex/RwLock.unwrap()` calls on concurrent request paths (IPC routing/provision, MCP cancellation) now recover from a poisoned lock instead of cascading a panic across the daemon.

## [1.9.2] - 2026-06-28

Remediation patch (P1, part 1): the LLM provider path + GUI RAG.

### Fixed
- **Real LLM providers are registered.** Storing an API key previously registered a `NoopProvider` (every completion errored). The daemon now builds the real adapter for the key's provider (Anthropic, Gemini, OpenAI, Groq, OpenRouter, Together); unknown families fall back to a NoopProvider with a logged warning rather than silently. This unblocks the provider-dependent features being wired in subsequent patches. (Mistral/Cohere/DeepSeek need a keychain-namespace bridge — flagged in logs.)
- **GUI RAG commands work.** The Tauri `rag_search`/`rag_list_sources`/`rag_index_stats`/`rag_ingest_file` commands were no-op stubs; they now call the daemon's `rag.*` IPC methods over the (now-working) IPC channel, with a typed `daemon_not_running` error instead of fake empty results.

### Known remaining (next patches)
HyDE, MCP sampling, the automation router, and board-debate still need the provider injected into their crates; RAPTOR/arch, real SPLADE/ColBERT, `live_cc` PTY, and plugin WASM ABI remain. Tracked via in-code `TODO`.

## [1.9.1] - 2026-06-28

Remediation patch (P0): make the flagship RAG retrieval and the CLI↔daemon IPC actually work in the shipped binary. A deep self-audit found several features were stubbed/fake despite being described as done; this release fixes the highest-impact ones.

### Fixed
- **Real embeddings in the daemon.** The daemon shipped `MockEmbedModel` (zero vectors), so RAG indexing/search was non-functional in the real binary. It now enables the `fastembed`/`reranker` features and uses a `LazyEmbedModel` that starts as a mock (instant daemon startup) and swaps in the real BGE/ONNX embedder via a background task; offline/uncached load falls back to mock gracefully. The cross-encoder reranker loads the same way. (Eager loading was rejected — it blocked startup past the readiness timeout.)
- **CLI↔daemon IPC works end-to-end.** Four unreconciled protocol layers fixed: frame length prefix aligned to big-endian (was LE on the daemon side); the `{auth,rpc}` envelope is now unwrapped; the daemon provisions the `~/.cascade/ipc_token` it never wrote; `ping`/`status`/`health` are routed through typed dispatch; and replies are wrapped in a JSON-RPC 2.0 envelope (`{jsonrpc,id,result|error}`) the CLI can parse. `cascade ping` now returns a real pong. The integration suite's previously-commented IPC assertions are restored.
- **FTS/dense retrieval return text.** `RetrievalHit` carried an empty `text`; results now join back to the chunks table for `text` + `file_path` + line numbers.
- **GUI cascade-doc commands.** `load`/`save`/`validate_cascade_doc` were stubs (blank/error/always-true); now read, atomically write, and actually validate.
- `infer_tier` is cross-platform (was macOS `/Users/`-only).

### Known remaining (tracked for follow-up patches)
LLM-provider-dependent features (HyDE, board debate, automation router, MCP sampling, `live_cc` PTY) still need the provider path wired; RAPTOR/arch summarisation, real SPLADE/ColBERT, and plugin WASM ABI remain. See in-code `TODO(<area>)` markers.

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
