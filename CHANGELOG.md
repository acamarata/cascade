# Changelog

All notable changes to cascade are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)

## [Unreleased]

### Fixed
- **The `rag.multi_vec` setting now actually affects search** (T-P7-E20-30). The
  global setting and the per-request `colbert_enabled` flag both existed but were
  never connected, so toggling multi-vec retrieval changed nothing. The daemon now
  resolves `[rag] multi_vec` from config and uses it as the default for requests
  that do not specify one; `colbert_enabled` became `Option<bool>` so an explicit
  `false` can still override a `true` global. Wire format is unchanged.
- **`RagSearchConfigOverride::default()` no longer disables keyword search.**
  `fts5_enabled` carried `#[serde(default = "bool_true")]`, which applies only
  when deserializing, so the derived `Default` produced `false` — Rust-side
  callers silently searched with FTS5 off while every deserialized request had it
  on, contradicting the field's own documentation and returning empty results
  that resembled an indexing failure.

### Changed
- **Model and subscription registry re-verified against first-party provider
  documentation** (2026-08-19). The registry had drifted 44 days and carried two
  factual errors, not just stale dates: Claude Fable 5 was recorded as "retired
  / blocked by Anthropic" when it has been generally available since 2026-06-09
  and is Anthropic's most capable widely released model; and Claude Opus 4.8 was
  recorded as the current best model when it is now listed as legacy, superseded
  by Claude Opus 5 — which was absent from the registry entirely.
  `MODEL_CLAUDE_OPUS` now resolves to `claude-opus-5`; `claude-opus-5` and
  `gemini-3.7-flash` were added to `models/models.yaml`; `verified_date` and
  `data/model-matrix.json` `_updated` are now 2026-08-19.
  Three referenced model IDs could not be confirmed from first-party sources and
  are explicitly annotated rather than assumed: `gemini-3.5-pro` and `gpt-5.5`
  appear in no current provider documentation, and `gemini-3.1-pro` is
  documented only as `gemini-3.1-pro-preview`. Sonnet 5's $2/$10 pricing is
  confirmed permanent — the scheduled 2026-09-01 increase will not occur.

### Fixed
- **`status` now reports `index_paused` truthfully** (T-P7-E20-29 completion).
  The IPC server holds the IndexManager and populates the field from real
  volume-pause state; previously the field was declared but never filled.
- Moved test-only model-id constant imports into their `#[cfg(test)]` modules,
  restoring `clippy -D warnings` after the model-id-gate refactor.

### Fixed
- **model-id-gate CI check restored to green** (2026-08-18). Twenty-one
  hardcoded canonical model-ID literals had accumulated across the daemon proxy,
  chat handlers and the GFP HTTP lane while CI was unable to run. All now import
  their constant from `cascade-types::model_ids`, so the canonical ids have a
  single source of truth again.

### Added
- **`cascade update --full` — one-command full-stack redeploy** (T-P7-E19-04,
  2026-08-18). A new `--full` flag on `cascade update` that composes the
  existing per-component update logic into a single orchestrated pass: daemon +
  CLI binary update (reuses `update apply` IPC), codesign the swapped binaries
  (macOS-only, ad-hoc, fails loudly if `codesign` tool missing), `launchctl
  kickstart -k` the daemon service (macOS-only, falls back to `cascade daemon
  restart`), models roster refresh (reuses `update models`), widget LaunchAgent
  re-install (reuses `cascade widget install`), and app signature verification
  (macOS-only health check via `codesign --verify --deep --strict`). The
  pipeline is idempotent, safe to re-run, and designed so a mid-sequence
  codesign failure still proceeds to kickstart (daemon not left down) while
  surfacing the error. Cannot be combined with a subcommand. 8 new tests;
  no existing tests or functionality changed.

### Added
- **Plugin enable/disable persistence helpers on `PluginsSettings`**
  (T-P7-E20-26, 2026-08-18). The `plugins.enabled` vector in `settings.json`
  has existed since T-P3-E07-14 and round-tripped correctly, but nothing
  consumed it — it was vestigial schema. Added `is_enabled`, `enable`, and
  `disable` methods on `PluginsSettings` (in the new
  `cascade_core::settings::plugins` module) so callers can query and toggle
  plugin enable/disable state through the existing `store::load`/`store::save`
  path. This complements (does not replace) the `.disabled` marker-file
  mechanism in `cascade-plugins`. 9 new tests cover the restart round-trip,
  idempotency, multi-plugin independence, and unknown/removed-plugin robustness.
  No behaviour change to existing functionality.

### Security
- **Removed maintainer-private identifiers from shipped files** (2026-08-18).
  A private maintainer email address was committed in
  `.github/docs/MODEL-MATRIX.md`, and maintainer-specific local filesystem paths
  appeared in `cascade-core`'s discovery doc comments and two `cascade-daemon`
  disk-guardian test fixtures. All are replaced with generic examples. The
  repository's own `scripts/check-no-maintainer-ids.sh` guard already covered
  these cases and was failing; it had simply never been run in CI.

### Fixed
- **RAG dense KNN now uses sqlite-vec DiskANN instead of a full table scan**
  (T-P7-E25-01, 2026-08-18). The production dense retrieval path now issues a
  vec0 `MATCH` query against the existing `rag_embeddings` store, backed by the
  vendored sqlite-vec 0.1.10-alpha.3 DiskANN implementation. Schema version 13
  transactionally rebuilds existing BLOB or flat-vec0 tables without losing
  rows, including databases previously opened by a non-`vec` build. sqlite-vec
  registration is centralised through `cascade-db`'s existing loader for
  production and test connections, and
  DiskANN's exact-rescore, missing-row, statement-cleanup, and connection-lock
  defects are corrected locally. Six targeted tests cover query-plan selection,
  schema/data migration, dense-only retrieval, and deterministic ANN quality;
  the measured fixture remains at 1.000 recall@10 and 1.000 exact-rank
  agreement.
- **OpenTelemetry tracing is no longer inert** (T-P7-E25-13, 2026-08-18). The
  OTel layer was constructed but never attached to the subscriber that actually
  gets installed, so every OTel span went nowhere. The provider is now created
  before logging init and composed into the live daemon subscriber. Telemetry
  remains strictly opt-in — the provider is `None` unless the existing
  `telemetry.enabled` gate is set, and the env filter, JSON file layer and
  stderr formatting are unchanged.

### Added
- **Test coverage for the personal vault UI and instructions browser**
  (T-P7-E21-01, T-P7-E21-02, 2026-08-18). 44 new tests across the encrypted
  personal-vault panel (collections, records, add-record, exposure log) and the
  instructions browser page (list, search, tier-diff views). Both surfaces
  already shipped; neither had direct component tests. No behaviour change.

### Added
- **Test coverage for the encrypted personal vault UI** (T-P7-E21-01, 2026-08-18).
  25 new tests for `PersonalEncryptedVaultPanel` and its backing hook, covering
  loading/error/empty states, collection selection, sensitivity badges, mode toggle,
  records table, exposure log, and add-record form validation (invalid JSON, array
  input, save error, successful submit clears textarea). Feature itself was already
  complete (`usePersonalEncryptedVault` + `PersonalEncryptedVaultPanel` wired into
  `/personal?tab=vault`); tests were the only gap.
- **Test coverage for the InstructionsPage component** (T-P7-E21-02, 2026-08-18).
  19 new tests for `InstructionsPage`, covering loading/error/header states, list
  view (tier cards, absent label, line count, expand/collapse), view-mode switching
  (List ↔ Search ↔ Diff), search results (match count, no-matches prompt), diff
  view (tier nav), and refresh trigger. Service layer (`instructionTiers.ts`) was
  already tested; the page component had no tests. Editing of tier files is not
  implemented — no Tauri write command for CASCADE.md/AGENTS.md exists in the
  backend; that gap is left open for a future ticket once the daemon handler is built.
- **Test coverage for the in-app chat prompt path** (T-P7-E21-03, 2026-08-18).
  22 new tests covering `buildSystemPrompt` (all three namespace branches) and
  the `ChatInput` component (disabled/streaming states, submit behaviour), both
  of which were exported and exercised on every chat request but had no direct
  tests. No behaviour change.
- **Cross-session dedup wired in `cascade.context_slice`** (T-P7-E15-01,
  2026-08-18). The `handle_context_slice` handler had an explicit TODO: every
  MCP session received fully independent results with zero deduplication across
  sessions because the live SQLite pool was never injected. `ToolRegistry` now
  carries an optional `DbPool` slot (mirroring the existing `RetrieverSlot`
  pattern) filled via `ToolRegistry::with_db_pool` / `db_pool_slot`. When a pool
  AND a `session_id` are present, `cascade.context_slice` runs
  `ContextOptimizer::cross_session_dedup` (filtering chunks already delivered to
  that session within the last 30 min via the `context_fingerprints` table)
  before assembly, and `ContextOptimizer::record_delivered` after the response is
  built — both in `spawn_blocking` (matching `handlers_memory`). The pool reuses
  the existing `cascade-db` `DbPool` / `build_pool` mechanism (no new
  persistence). Without a pool or `session_id`, behaviour is unchanged. Response
  metadata gains `dedup_applied` and `dedup_suppressed`. 3 new tests prove dedup
  ACROSS two simulated sessions (same `session_id`), session-scoping (different
  `session_id` not suppressed), and the no-pool passthrough. Also a one-line
  clippy `io_other_error` fix in `cascade-db::pool::build_pool` (`Error::other`,
  MSRV-safe since 1.74) surfaced by the new dependency.

### Changed
- **Production JS bundles code-split to clear Vite's 500 kB chunk warning**
  (T-P7-E17-01 / T-P7-E17-02, 2026-08-18). Both Vite apps previously shipped
  every route and every vendor dependency in a single oversized chunk. Route
  components are now loaded on demand via `React.lazy` + `Suspense`, and heavy
  vendor libraries are split into dedicated chunks via
  `build.rollupOptions.output.manualChunks`. No routes, UI, or behaviour
  changed — this is a bundling-only change. Measured results (minified / gzip):

  - **cascade-app**: the single entry chunk went from **2,679 kB (756 kB gzip)**
    to **58 kB (19 kB gzip)**. The largest vendor chunk is `vendor-charts` at
    370 kB (110 kB gzip); all other vendor chunks (CodeMirror core/view/lang/ext,
    markdown, highlight, react, radix, flow, icons, router, tauri) are under
    270 kB. No chunk exceeds 500 kB — the Vite warning is gone without raising
    `chunkSizeWarningLimit`. CodeMirror language modes were investigated as a
    suspected contributor but verified **not** the cause: `@codemirror/language-data`
    and `highlight.js` already lazy-load their language parsers via dynamic
    imports (visible as the many small per-language chunks in the build output).
    The baseline monolith was dominated by eagerly-bundled vendor libraries and
    all route code in one file.
  - **cascade-dashboard**: the single entry chunk went from **1,004 kB
    (304 kB gzip)** to **42 kB (14 kB gzip)**. The largest vendor chunk is
    `vendor-charts` at 318 kB (94 kB gzip); all other vendor chunks (react,
    highlight, markdown, radix, router, date, icons) are under 176 kB. No chunk
    exceeds 500 kB.

### Removed
- **Dead `HttpTransport` / `SseTransport` stubs removed from cascade-mcp**
  (T-P7-E15-03, 2026-08-18). Two legacy P2 transport stubs with no callers
  anywhere in the workspace, plus the imports they alone pulled in. The
  production axum-based `HttpServer` and `SseServer` are untouched — these were
  separate, unused types that only made the transport module harder to read.

### Added
- **Build progress panel shows the real fleet dispatch actor instead of a
  placeholder** (T-P7-E10-05, 2026-08-18). When a project is actively
  building, the Cascade.app build progress panel previously displayed a
  hardcoded `fleet: —` stub next to the elapsed timer, with a TODO noting
  the fleet account/model was not yet in the API. The panel now reads the
  `last_dispatch_actor` field that T-P7-E10-06 added to
  `GET /api/projects/:id/phase` and renders it readably: a fleet actor
  string like `fleet/claude/BulkExec` is parsed and shown as
  `claude · BulkExec` (CLI binary · task class), and the full raw actor is
  kept in the element tooltip. When no fleet dispatch has been recorded
  yet — the field is null — the panel says so honestly (`no dispatch yet`)
  rather than inventing or predicting a CLI from the ticket's weight. The
  `DaemonPhaseStatus` TypeScript interface gains the two additive fields
  (`last_dispatch_actor`, `last_dispatch_task_class`); existing fields are
  unchanged. The separate TODO to replace the 10 s poll with an SSE/Tauri
  build stream is deliberately left in place, since the daemon endpoint it
  depends on does not yet exist.
### Removed

- **Dead `HttpTransport` / `SseTransport` channel-backed stubs removed from
  `cascade-mcp`** (T-P7-E15-03, 2026-08-18). Both were self-documented P2
  legacy stubs with zero callers outside their own definition files. Confirmed
  by workspace-wide grep before deletion. The production axum-based
  `HttpServer` and `SseServer` (P4) in the same files are untouched. The
  now-unused `use super::Transport;` imports and `debug` / `mpsc` imports
  that were only pulled in by the dead types are also removed. Test counts
  are unchanged (5 in `http.rs`, 4 in `sse.rs`) because no tests referenced
  the dead types.

### Fixed
- **Release workflow no longer pins pnpm to a major version behind the
  lockfile** (T-P7-E25-09, 2026-08-18). `release.yml` pinned
  `pnpm/action-setup` to `version: 9` while the repo's `packageManager` field
  declares `pnpm@10.30.1`, so release builds ran a pnpm major version older
  than the one that generated the lockfile. The pin is removed; the action now
  resolves from `packageManager`, matching every other workflow in the repo.

### Added

- **Real fleet dispatch actor is now recorded and exposed** (T-P7-E10-06,
  2026-08-18). The build engine's fleet dispatch path previously logged every
  step transition under a hardcoded `"cascade-cli"` actor, so nothing on disk
  said which fleet CLI actually executed a ticket. The engine now resolves
  the executor it is about to use (`classify_ticket` +
  `cli_binary_for_task_class`, unchanged) and records it on the step events
  it emits via the new additive `PbdStore::transition_step_as` — actor format
  `fleet/<cli-binary>/<TaskClass>`, e.g. `fleet/claude/BulkExec`. Manual/CLI
  callers keep the `"cascade-cli"` default through the unchanged
  `transition_step`. The daemon's `GET /api/projects/:id/phase` response
  gains two additive fields, `last_dispatch_actor` and
  `last_dispatch_task_class`, read from the project's `events.jsonl` (most
  recent step-level event; non-fleet actors report no task class).
  Observability only: dispatch, classification, routing, retry, and the
  `CASCADE_STEP_COMPLETE` marker protocol are untouched.
- **Injectable `FleetRunner` seam enables deterministic end-to-end tests of
  the real dispatch path** (T-P7-E03-06, 2026-08-18). The build engine's
  fleet dispatch now shells the CLI through a `FleetRunner` trait
  (`fn run(&self, task_class, prompt) -> FleetOutcome`, `Send + Sync`) with a
  `RealFleetRunner` that delegates to the existing `run_fleet_cli`. Default
  construction is unchanged — `cascade build run --real` behaviour, including
  the `CASCADE_STEP_COMPLETE:<step-id>` marker protocol, retry/backoff, step
  transitions, and gate checks, is byte-for-byte identical. Tests may opt in
  via `BuildEngine::with_fleet_runner` to drive the full real path on a toy
  phase with a scripted runner (no network, no live CLI); a separate
  `#[ignore]`d live test can be enabled with `CASCADE_E2E_LIVE=1`.

### Fixed
- **Version numbers across the app manifests now track the real product
  version** (T-P7-E16-01, T-P7-E16-02, 2026-08-18). `apps/cascade-app` and
  `apps/cascade-dashboard` reported `0.9.3` in their `package.json`, and the
  Tauri crate hardcoded `1.5.1`, while the product was on `1.16.0`. The Tauri
  crate now inherits both `version` and `rust-version` from the workspace, its
  seven stale intra-repo path-dependency pins were refreshed, and both
  `package.json` files report the real version. `tauri.conf.json` was already
  correct and is unchanged.

### Changed
- **Declared MSRV corrected to 1.88 and now actually enforced in CI**
  (T-P7-E16-04, 2026-08-18). `rust-toolchain.toml` documented an MSRV of 1.75
  while the workspace `Cargo.toml` declared 1.85. Both were wrong: locked
  dependencies (`darling`, `image`, `plist`, `time`, `serde_with` and others)
  declare `rust-version = 1.88`, so the workspace has not been buildable on
  1.85 for some time — nothing ever verified the number, because every CI job
  installs `@stable`, which floats far above the MSRV and never exercises it.
  The workspace now declares 1.88, the toolchain comment matches, and a new
  `msrv` CI job pins `dtolnay/rust-toolchain@1.88` and runs
  `cargo check --workspace --all-targets --locked` so the claim is verified
  rather than asserted. Consumers on 1.85 were already unable to build this
  workspace; the declaration now says so honestly.

### Security
- **Audit closure: `cascade-agy` OAuth credential findings reviewed and accepted**
  (T-P7-E16-05, 2026-08-18). `src/bin/cascade-agy` and `src/bin/cascade-agy-auth`
  embed a Google OAuth client id and secret. These are credentials for an
  *installed/desktop* OAuth client, not confidential server credentials: the
  client authenticates via a loopback redirect
  (`http://localhost:51121/oauth-callback`), which Google permits only for the
  Desktop application client type, and RFC 8252 classifies such native clients as
  public clients whose "secret" cannot be kept confidential and is not treated as
  one. They are allowlisted in `.gitleaks.toml` with that rationale. No user
  credential, API key, or server secret is exposed. Separately, the reported
  `security.yml` artifact-upload error is not reproducible: the workflow exists
  and contains no upload-artifact step.

### Fixed
- **Model weights are no longer downloaded into ephemeral storage**
  (T-P7-E25-18, 2026-08-18). Cascade resolves its model cache to
  `CASCADE_MODEL_DIR`, falling back to `$HOME/.cascade/models`. Any process that
  pointed `HOME` at a temporary directory therefore redirected multi-GB
  embedding/reranker downloads into throwaway storage: each run started from an
  empty cache and re-fetched roughly 2 GB, and whatever still held the files
  open when the directory was removed was left behind. A single day of test runs
  accumulated 32 such orphaned directories totalling 8.83 GB and filled the
  disk. Cascade now refuses to hand fastembed a cache directory that lives
  inside the OS temp directory, failing fast with a clear error before any
  download starts. Downloading gigabytes into the temp directory is never
  correct, so the guard is unconditional rather than test-only; set
  `CASCADE_ALLOW_TEMP_MODEL_DIR=1` to opt back in. Successful-download
  behaviour, model validation, and the `~/.cascade/models` default path are
  unchanged.

### Added
- **Tray "Pause daemon" now actually pauses background work**
  (T-P7-E05-04, 2026-08-18). Clicking *Pause* in the menu-bar tray used to
  flip an internal flag that nothing read — polling, indexing, and
  auto-updates continued unchanged. The flag is now honoured for real: the
  10-second status/tray refresh loop skips its fetch while paused, the
  24-hour periodic self-update check skips its work tick, and RAG indexing is
  paused through the same APIs the external-volume watcher already uses —
  the index manager stops registering sources and the file watcher stops
  forwarding change events to the indexing pipeline until you unpause.
  Everything else keeps running normally while paused: the local IPC socket,
  the dashboard, and the memory/disk safety guardians stay fully responsive,
  and unpausing resumes each loop on its next scheduled tick with no restart
  and no state rebuild. The old unit test that merely asserted the flag
  could be toggled is replaced by behavior tests: one proving the tray loop
  emits no updates during a paused window and resumes afterwards, and two
  proving indexing pause/resume is genuinely invoked (state and end-to-end
  signal suppression).
- **Real Anthropic request-level failover proxy, auto-started on
  `127.0.0.1:3763`** (T-P7-E13-09, 2026-08-17) — a behavior-affecting
  change for anyone who activates it. The daemon now starts an
  unconditional loopback proxy (alongside `:3761`/`:3762`) that serves the
  Anthropic Messages API and dispatches each request as a local `claude`
  subprocess under the account chosen by
  `cascade_core::selection::select_account` (the same spill order as the
  conductor). On the empirically captured failure signatures (2026-08-17
  captures under /tmp/cascade-failover-capture; see the module doc in
  `crates/cascade-daemon/src/proxy/anthropic_failover.rs`) — first
  `system/api_retry` 429 line, synthetic `assistant` error markers
  (`authentication_failed`, `rate_limit`), terminal `result` with
  `is_error:true` — the attempt is abandoned pre-commit and the next
  account is tried, so the caller sees one continuous, correctly-framed
  Anthropic SSE stream. Stream output is re-framed from Claude Code's
  `--output-format stream-json` NDJSON into literal Anthropic SSE events
  (new `stream_json` classifier; not the Gemini-oriented
  `anthropic_compat/sse.rs`). Post-commit failures surface as an Anthropic
  `error` SSE event, never a silent truncation. Full spill exhaustion
  returns HTTP 529 `overloaded_error` naming every tried account. Child
  environments are sanitized (`ANTHROPIC_*`/`CLAUDE*` stripped) so the
  proxy never recursively proxies itself or leaks the caller's
  credentials into another account's config dir.
  **BEHAVIOR CHANGE / BILLING:** activation is an explicit user step —
  `export ANTHROPIC_BASE_URL=http://127.0.0.1:3763` before `claude`
  (README § "Interactive-session failover proxy"; `cascade doctor` shows
  reachability + the activation one-liner; no shell config is ever
  mutated). Once active, interactive-session usage is billed against
  whichever fleet account the proxy selects (zai/GLM lane first, then
  Claude accounts), not necessarily the account originally logged in.
  Fidelity limits for text-chat traffic: tool calls render as text
  markers; image/document blocks are not forwarded; sensitive prompts
  still never land on untrusted lanes. The old fail-closed skeleton test
  is replaced by 27 real bind+serve+spill+SSE re-framing tests.

### Changed
- Conductor spill loop (`execute_with_fallback`) now re-reads
  `~/.cascade/accounts/quota.json` before each spill-target selection (CFC-05)
  instead of routing against the snapshot loaded once at startup — a lane
  saturated mid-run by another process (daemon utilization update, concurrent
  conductor run) is now skipped. The re-read is best-effort: any I/O or parse
  failure (missing file, torn JSON from a concurrent writer mid-write) falls
  back to the in-memory startup snapshot rather than emptying the spill pool,
  and the typed tried-lane bookkeeping (T-P7-E13-02) is re-applied on top of
  the refreshed snapshot — now marking ALL tried lanes, not just the most
  recent failure, which also fixes premature spill exhaustion when two or more
  lanes had already failed. Verified 2026-08-15: build/test/clippy clean.
  (T-P7-E13-07)

### Added
- **Periodic auto-update loop** (T-P7-E12-06, 2026-08-15): the daemon now
  runs a real 24-h background loop when `[updates] auto = true`. Every tick
  calls `check_for_update` / `apply_update` for the binary and separately
  refreshes `~/.cascade/models.yaml` from GitHub. The loop starts 10 minutes
  after daemon boot to avoid hitting the network on every restart. All fetch
  failures (including the private-repo 404 documented in T-P7-E12-04) are
  non-fatal: logged at WARN, compiled-in `models.yaml` fallback preserved,
  loop continues at the next 24-h cadence. `get_auto_update` in
  `cascade-daemon/src/updates/ipc_handlers.rs` now has a real production
  caller; its `#[allow(dead_code)]` attribute has been removed.
- Conductor `execute_*` dispatches (`claude`/`codex`/`opencode`/`agy`) are now
  wrapped in a real subprocess timeout (`LANE_TIMEOUT_CLI`/
  `LANE_TIMEOUT_GEMINI`, 300s each, matching agy's own `--print-timeout`
  default) instead of blocking the whole spill chain forever on a hung
  external CLI process. Ticket asked for `tokio::time::timeout`, but the
  execute_* path is synchronous `std::process::Command`, which that can't
  interrupt — used the existing spawn+`try_wait`+kill pattern instead, the
  correct synchronous equivalent. A timeout is now a normal spillable
  failure (`"timed out after Ns"`), feeding the same loop-guard as any other
  unavailable lane. (T-P7-E13-03)
- New `cascade conductor fanout` mode: dispatches a single prompt to N
  distinct accounts in parallel (spill order, sensitivity firewall
  respected) and aggregates results — first-success by default, or every
  result with `--all`. Purely additive; the existing sequential
  `execute_with_fallback` spill path is unchanged and remains the default.
  (T-P7-E13-04)

### Changed
- Audited the HyDE and feedback-training TODO deferrals without changing their
  implementation. The repository has no current `DEFERRED-BACKLOG.md`; its only
  matching file is the explicitly superseded
  `DEFERRED-BACKLOG-2026-06-18.md`. That archive and current E-P7-20 ticket
  `T-P7-E20-01` cover HyDE activation, but neither the current E-P7-20 roadmap
  scope nor its 28 tickets covers the feedback-trained projection/LoRA pass.
  The missing feedback-training cross-reference is therefore recorded as a
  planning discrepancy rather than falsely closed. (T-P7-E14-06)
- Corrected the RAG embedding model's public labeling across docs, UI copy,
  configuration guidance, logs, and rendered Rust documentation: the stable
  `bge-m3` provider/configuration key currently runs Multilingual E5 Large for
  1024-dimensional dense vectors, TF-IDF for sparse vectors, and an optional
  per-word E5 proxy for MaxSim multi-vector retrieval. It no longer presents
  those paths as native BGE-M3/SPLADE/ColBERT; true support remains tracked in
  E-P7-20. Internal enum names, cache keys, and configuration compatibility are
  unchanged. (T-P7-E14-02)
- Nomic/Jina embedding providers now fail loud at provider-selection time
  instead of surfacing a generic `EmbeddingFailed` deep in the embed call
  path. `NomicProvider::new`/`JinaProvider::new` return
  `Err(CascadeError::Other)` with a clear "not available in this build"
  message referencing E-P7-20, and a new
  `cascade_rag::embed::validate_provider_kind(ProviderKind)` capability
  check rejects Nomic/Jina at config-validation time. The `EmbedModel`
  methods still return `EmbedError::Unimplemented` as a defense-in-depth
  fallback. Full Nomic/Jina implementation remains tracked in E-P7-20.
  (T-P7-E14-01)
- Router unification (T-P7-E13-01): confirmed `crate::selection` was already
  the canonical account-selection module (an earlier E1-S6 pass had already
  reduced `conductor_router` to a thin wrapper and folded `RoutingTable`'s
  role into feeding live GP-pool health, not competing routing). Closed the
  one remaining real gap — `select_target_for_prompt`'s sensitivity-firewall
  logic still lived inline in `conductor_router.rs` — by moving it verbatim
  into a new `selection::select_account_for_prompt`; `conductor_router` is
  now 100% pass-through delegation with zero owned selection logic. No
  routing decisions changed: verified via test-name diff (863 → 866, zero
  removed, 3 added) and confirming all 4 pre-existing sensitivity tests
  (which exercise real behavior through the shim) still pass unchanged.
- Cursor and Antigravity provider adapters now honestly reflect their
  detect+config-only scope: the unreachable `inference_routing` cfg-feature
  stub blocks ("stub — not implemented") and the now-meaningless
  `inference_routing` Cargo feature were removed; `complete`/
  `complete_stream` unconditionally return an accurate
  `UnsupportedTaskType` error; and `available_models` returns an empty list
  (per the `ProviderAdapter` contract it feeds the routing model-picker, and
  no subscription model is routable through these adapters). Antigravity's
  module doc now also disambiguates it from the real `agy`-CLI Gemini
  dispatch path in `cascade-cli`'s conductor. (T-P7-E12-01, T-P7-E12-02)

### Added
- `cascade build run --real` now treats fleet-agent exit status and explicit
  `CASCADE_STEP_COMPLETE:<step-id>` stdout markers as the authoritative step
  result, persists `pending -> running -> passed/failed` transitions, and runs
  injected external CR/QA checks before accepting completion. (T-P7-E03-03)
- Real fleet builds now resume from persisted step status without redispatching
  `passed`/`skipped` work, recover interrupted or failed steps, and retry
  transient nonzero CLI exits at most three times before persisting failure.
  (T-P7-E03-05)
- Added an opt-in, temp-isolated three-ticket `cascade build run --real` CLI
  acceptance harness plus a matching MockDispatcher toy-phase integration test.
  The real harness verifies agent-created file contents as well as persisted
  ticket and step states; it remains gated on an available cheap-tier account.
  (T-P7-E03-06)

### Added
- Added cross-platform tray `OpenApp` and working `PauseDaemon` to
  `cascade-daemon`. `OpenApp` now handles Linux (`xdg-open`) and Windows
  (`explorer`) in addition to the existing macOS (`open -a`) handler.
  `PauseDaemon` toggles a new process-global `DAEMON_PAUSED: AtomicBool`
  flag and logs the pause/resume transition; supervisor and fleet-poller
  loop wiring to respect the flag is deferred to a follow-on ticket.
  (T-P7-E05-04)
- Wired quota IPC through `DaemonState` and real `BudgetConfig` in
  `cascade-daemon`. `IpcServer` now carries `daemon_state:
  Arc<Mutex<DaemonState>>` and `budget_config: BudgetConfig` loaded from
  `config.toml` at startup. `update_quota_state` pushes typed snapshots into
  the shared `DaemonState` buffer before aggregating and writing
  `quota-store.json`; `budget_check` uses the live `BudgetConfig` instead of
  an all-disabled default. (T-P7-E05-01)
- Implemented `gci_write`, `symlink_create`, `symlink_delete`, and
  `key_rotation` IPC methods in `cascade-daemon`. `gci_write` atomically
  writes a file to `~/.claude` and creates sibling symlinks
  (`CLAUDE.md`/`AGENTS.md`/`.cursorrules`/`.aider.md` → `CASCADE.md`) when
  the target is `CASCADE.md`. `symlink_create`/`symlink_delete` manage
  named symlinks inside `~/.claude` with traversal guards. `key_rotation`
  generates a new IPC auth token and writes it to disk (effective after
  restart). All four methods emit audit records on every code path.
  Added `ipc_resolve_gci_target` and `ipc_write_atomic` private helpers
  plus 4 unit tests. Updated `tests/audit_instrumentation.rs` to match
  real handler responses instead of the removed METHOD_NOT_FOUND stubs.
  (T-P7-E05-03)

- Implemented real fleet_poller sources for `ClaudeMaxSource`, `CodexSource`,
  and `AgySource` in `cascade-daemon`. `ClaudeMaxSource` now reads
  `~/.claude/usage-cache.json` (written by `fetch_and_cache_claude_usage` in
  the same tick) and returns a `QuotaState` with per-account `pct_used` values
  from `five_hour.utilization`. `CodexSource` and `AgySource` probe CLI
  presence via `detect_cli` and return `Some` (empty models) when the binary
  is on `$PATH`, `None` otherwise. All three now contribute to
  `quota-store.json` when their data is available. Renamed
  `stub_sources_return_none` test to `sources_return_none_when_unconfigured`;
  added `claude_max_source_reads_from_cache` and
  `claude_max_source_skips_auth_failure_entries` tests. 552 lib tests pass.
  (T-P7-E05-05)
- Added `NoopReason` enum (`OAuthPending`, `GenericEndpoint`, `UnknownFamily`)
  to `cascade-daemon`. `ProviderIpcHandler` now tracks why each slot was
  downgraded to `NoopProvider` and surfaces `"configured_not_connected"` with
  a human-readable detail string in `providers_list()`. Previously all noop
  slots reported `"unknown"` (indistinguishable from a provider that simply
  hasn't been health-checked). All three call sites are wired:
  `providers_oauth_start` background task → `OAuthPending`;
  `providers_add_generic` → `GenericEndpoint`; `providers_add_apikey` for
  unknown family → `UnknownFamily`. `providers_remove` clears the reason.
  Added 3 unit tests; 19 ipc_providers tests pass. (T-P7-E05-06)

### Fixed
- The daemon's lazy embedding fallback is now observable instead of silently
  degrading retrieval quality. `LazyEmbedModel` tracks `loading`, `ready`, and
  `degraded` states; a failed real-model init (or explicit offline-model mode)
  atomically engages the degraded flag and logs a warning naming the active
  `MockEmbedModel` fallback. Authenticated daemon status IPC exposes the state,
  and `cascade status` / `cascade status --json` show the degraded condition
  (and return a failing status while it is active). A simulated init-failure
  test captures and asserts the warning. (T-P7-E14-05)
- BGE reranker requests now fail with an explicit capability error when
  `cascade-rag` is compiled without the `reranker` feature. The disabled path
  no longer returns linearly decreasing fake relevance scores; a targeted
  no-feature regression test invokes the real `BgeReranker` path. The explicit
  `NoopReranker` remains available for callers that intentionally select it.
  (T-P7-E14-04)
- Dense embedding requests now fail with an explicit `EmbeddingFailed` error
  when `cascade-rag` is compiled without the `fastembed` feature. The low-level
  embedder no longer returns all-zero vectors that could silently pollute
  similarity results, and feature-disabled tests cover both the `EmbedModel`
  and public `EmbeddingProvider` paths. (T-P7-E14-03)
- Conductor's spill loop-guard (T-P7-E13-02) used fragile `starts_with`
  prefix matching to detect whether an account had already been tried
  (`tried.iter().any(|t| t.starts_with(&next.account_id))`), which had a
  real bug: a tried entry for `claude2` false-matched the prefix check for
  `claude`, terminating the spill chain one lane early. Replaced with a
  typed `LaneFailure`/`TriedLane` classification and exact account-id
  equality; the exhausted-message and JSON `fallbacks_tried` output formats
  are unchanged. No other spill/fallback decision logic changed.
- Removed dead `cascade_resolve` arm from the post-dispatch audit hook in
  `cascade-daemon` IPC. The arm was unreachable: the real `cascade_resolve`
  handler returns before reaching the audit match block, so the arm was
  silently emitting a spurious "handler pending E-07+" audit entry that would
  never fire. Deleted the dead arm; all tests pass. (T-P7-E05-02)
- `cascade link --tool aider` now creates `.aider.md` (the instruction file Aider
  reads via `--read`) instead of `.aider.conf.yml` (Aider's tool config file, which
  the spec explicitly leaves independent). Spec references: `04-cascade-nomenclature-spec.md`
  § 2, `06-cascade-code-apps-support.md` Aider row. (T-P7-E23-03)
- `CascadeTierTree`'s "Create" button now actually scaffolds the missing tier's
  `.cascade/` directory (subdirs, skill suite, agents, `CASCADE.md`, tool symlinks,
  `.gitignore`) instead of only opening the parent directory in Finder. The
  scaffolding reuses `cascade init`'s own logic via a new shared
  `scaffold_ai_folder()` function and a new `create_cascade_tier` Tauri command,
  so a tier created from the desktop app is byte-identical to one created from the
  CLI. (T-P7-E10-03)
- VaultContext's dev-fixture fallback now requires both Tauri-runtime absence AND
  `import.meta.env.DEV` (previously gated only on Tauri absence, so a production
  build outside a Tauri webview could serve fake vault data). A visible amber
  `DevFixtureBanner` is now rendered whenever fixture data is active. (T-P7-E10-04)
- Fixed a one-token account-id typo in Conductor's fleet spill order:
  `ACCOUNT_SPILL_ORDER` referenced the Gemini/agy account as `"gemini-agt"`,
  but the real account in `~/.cascade/accounts/quota.json` is `"gemini-acc1"`.
  The exact-string match at spill-order resolution never matched, so the
  Gemini/agy lane was silently unreachable in natural (non-override) spill
  selection — `--account gemini-acc1` (the override path) always worked,
  masking the bug. The two unit tests exercising this path used the same
  wrong name in their own fixtures, so they passed against themselves without
  catching the mismatch against the real account name. Added a regression
  test that checks the spill order directly against the real name so this
  can't silently reintroduce.

### Security
- Fixed `cascade_core::security::audit`'s hash-chain tamper-evidence: its
  `sha256_hex` was a stub that hashed only the input's byte length
  (`format!("{:064x}", input.len())`), so any two audit entries of equal
  serialized length produced identical "hashes" and a forged same-length
  entry passed `verify_chain`. Now uses real `sha2::Sha256` (already a
  workspace dependency). Added a regression test proving a forged
  same-length entry is now rejected. This module is actively used by 5
  cascade-core security modules (env_allowlist, sanitizer, mcp_envelope,
  rag_poisoning, validator) — it was not dead code, contrary to
  T-P7-E11-05's original premise; ticket closed as premise-invalid rather
  than deleted, with this narrower fix applied instead. Migrating those 5
  call sites onto the separate `cascade-audit` crate (a different API
  shape) remains a distinct, unscoped follow-up. (T-P7-E11-05)
- Removed the unused `cascade_core::security::oauth` module, whose HMAC and
  SHA-256 functions were explicitly fake placeholders. Production OAuth/PKCE
  remains in `cascade-providers::oauth` and is unchanged. (T-P7-E11-04)
- Removed the unused `cascade_core::watcher` scaffold and its misleading
  no-op `CascadeWatcher`; the functional daemon `rag_watcher` and
  `volume_watcher` implementations remain unchanged. (T-P7-E11-03)
- Changed `fs_exec` and `net_listen` plugin capability requests from silently
  ineffective declarations to explicit `CapabilityError::Reserved` failures,
  consistent with the other unimplemented reserved capabilities. (T-P7-E11-02)
- Replaced the `cascade-plugins` WASM host's hardcoded log stub and empty/no-op
  KV imports with bounds-checked guest-memory access and a per-plugin SQLite
  store at `<plugin-dir>/data/plugin-kv.sqlite3`. Both `LoadedPlugin` call paths
  now register the host imports; an integration fixture verifies the actual
  guest log message and KV persistence across calls and reload. (T-P7-E11-01)
- Fixed a `.gitleaks.toml` scope violation: an out-of-scope pass had blanket
  allowlisted 6 paths beyond the single ticketed finding
  (`crates/cascade-core/src/middleware/post.rs:539`), including 2 files
  (`src/bin/cascade-agy`, `src/bin/cascade-agy-auth`) without individual
  verification. Re-verified every entry individually by removing the
  allowlist and re-running `gitleaks detect` raw: all 6 are confirmed
  non-secrets (synthetic AWS/API-key-shaped test fixtures in
  `client_leak.rs`/`secret_scan.rs`/`tool/tests.rs`/`redaction.rs`, and
  Google's publicly-documented Antigravity desktop OAuth client id/secret in
  `cascade-agy`/`cascade-agy-auth` — not production credentials). Each entry
  now has an accurate, verified rationale comment instead of a blanket
  description. `gitleaks detect --source . --no-git` now reports "no leaks
  found".
- Bumped `ring` 0.17.9 -> 0.17.13/0.17.14 to clear RUSTSEC-2025-0009 (AES
  encryption panic when overflow checking is enabled). `cascade-providers`
  directly depends on ring; transitive dependency in the workspace. Pinned to
  ^0.17.13 in `crates/cascade-providers/Cargo.toml` and workspace-wide in
  `Cargo.lock`. Verified: only one ring version in workspace (0.17.14 via
  Cargo.lock resolution of ^0.17.13).
- Bumped transitive `quinn-proto` 0.11.14 -> 0.11.16 to clear RUSTSEC-2026-0185
  (remote memory exhaustion, high severity). No direct workspace dependency on
  `quinn`/`quinn-proto` exists; the fix is a `Cargo.lock`-only update via
  `cargo update -p quinn-proto`.
- Bumped `pdf-extract` to 0.12 (replacing `lopdf` 0.34) in `crates/cascade-rag`,
  gated behind the optional `pdf-parser` feature, to clear RUSTSEC-2026-0187.
- Bumped `wasmtime`/`wasmtime-wasi` 36.0.11 -> 36.0.12 (exact pin in the
  workspace root and `crates/cascade-pdk/Cargo.toml`) to clear
  RUSTSEC-2026-0188 (WASI hard-link/rename FilePerms bypass).
- Reconciled transitive `quick-xml` versions: `plist` 1.9.0 -> 1.10.0 (pulled
  in by `tauri`) now shares the 0.41.0 line already used by `calamine`,
  clearing RUSTSEC-2026-0194/0195 for two of the three previously co-installed
  versions. The third, `quick-xml` 0.36.2 via `docx-rs` 0.4.20, has no
  upstream fix available (docx-rs still pins `^0.36`); added to the
  `cargo-audit` ignore-list in `.github/workflows/security.yml` with a
  documented risk rationale (DoS-class only, local user-supplied DOCX
  ingestion, no RCE/memory-corruption path).
- Bumped `memmap2` 0.9.10 -> 0.9.11 (RUSTSEC-2026-0186, unsound pointer
  offset) and `spin` 0.9.8 -> 0.9.9 (yanked version).
- Added `RUSTSEC-2017-0008` (serial) and `RUSTSEC-2026-0192` (ttf-parser) to
  the `cargo-audit` ignore-list — both unmaintained with no newer release
  available; documented alongside the existing GTK-tray-icon-chain entries.
- Fixed `apps/cascade-dashboard`'s `vite`/`vitest` (5.3.4/1.6.1 -> 6.4.3/3.2.6,
  matching `cascade-app`'s already-fixed versions) and `@vitest/coverage-v8`
  (mismatched 4.1.8 pinned against vitest 1.x -> 3.2.6 to match) — cleared
  the critical Vitest-UI arbitrary-file-read advisory and the Vite Windows
  `server.fs.deny` bypass, plus their transitive esbuild findings. `pnpm
  audit` now reports zero vulnerabilities workspace-wide.
- Removed the hardcoded `pnpm/action-setup@v2` `version: 9` pin from the
  `npm-audit` CI job (`.github/workflows/security.yml`) in favor of
  `corepack enable`, matching the rest of the workspace's CI convention (see
  `ci-standard.md`: pinned pnpm versions desync from the lockfile and break
  CI when pnpm releases a new major).

### Fixed
- **Documentation**: Audited the four launch wiki pages against the current CLI
  and installer source; corrected installer/init semantics, tier paths and merge
  order, generated harness outputs, and stale or nonexistent command syntax.
- `cascade-daemon`: `CeoRuntime::build_executor` (`ipc_ceo.rs`) now dispatches
  tool calls through the real `LocalToolInvoker` (wrapped in `SafetyGate` for
  deny-list + path-sandbox + real `cascade-audit` logging) instead of the
  duplicate `FallbackInvoker`, which has been deleted. Tool dispatch no longer
  depends on whether a `ProviderRegistry` is wired — only the LLM-step router
  (`RegistryRouter` vs. `FallbackRouter`) still branches on that.
- Reordered `daemon-ci.yml`'s "Assert lean binary excludes network surfaces"
  step to run immediately after the lean/default-features build, before the
  subsequent all-features build overwrites `target/debug/cascaded` — the
  assert was silently checking the all-features binary, causing a false
  "network symbols found" failure on every run.
- Split `ci-app.yml`'s `build-matrix` job (macOS/Ubuntu/Windows Tauri builds)
  into `build-linux` (self-hosted, always runs) and `build-hosted`
  (macOS/Windows, gated behind `vars.HOSTED_CI`) — this is a private repo, so
  the previous unconditional matrix was spending paid GitHub-hosted minutes
  and failing outright once the account's spending limit was hit.
- Fixed 5 React purity/refs lint violations in `apps/cascade-app`
  (`useAccounts.ts`, `useChat.ts`, `usePewsTree.ts`, `useIngestProgress.ts`):
  moved `Date.now()` calls and ref writes out of the render body into
  effects or callback-time state, per `react-hooks/purity` and
  `react-hooks/refs`.
- Fixed an intermittent `markdownPreview.test.tsx` flake (reproduced in
  ~20-30% of isolated runs): a fire-and-forget `userEvent.click(...)` could
  reject after the test's jsdom environment tore down. Switched to the
  synchronous `fireEvent.click`.
- Fixed `cascade-mcp`'s `c_harness_setup_prompt` e2e test, which depended on
  ambient dev-machine state (`~/.claude/CLAUDE.md`) rather than a fixture —
  passed locally for every developer but failed on a clean CI runner HOME
  with no cascade tier files present. Now uses the same fixture HOME as
  `a_resource_surface`.

### Changed
- **Documentation**: Corrected `04-cascade-nomenclature-spec.md` § 5 wizard phase
  count from 8 to 10 phases to match the real `WizardStep` enum
  (`Welcome=1..Done=10`, confirmed in `types.ts`, `stepLabels.ts`, and 10 phase
  component files under `phases/`). `05-cascade-product-architecture.md` § 5 is
  now the declared canonical source for wizard phase detail; doc 04 § 5 defers to
  it.
- **Documentation**: Reconciled the obsolete future `v0.1.0` FOSS-launch
  framing with the workspace's 1.16.0 version line. The public repository and
  public CI mean the FOSS public launch is complete; remaining signing,
  enrollment, and package-channel work is post-launch distribution hardening.
- **Models**: Updated GPT fleet routing to OpenAI's GPT-5.6 family (Sol, Terra, Luna). `MODEL_GPT` now defaults to the flagship `gpt-5.6-sol`, replacing `gpt-5.5`. Added `MODEL_GPT_TERRA` and `MODEL_GPT_LUNA` constants. Updated `models.yaml` context windows (1.05M tokens) and pricing for all GPT-5.6 variants.
- **Updates**: Added `cascade update models` to refresh the cached fleet roster
  in `~/.cascade/models.yaml`; daemon model-drift checks now prefer that valid
  cache before falling back to the embedded default.
- **Build**: `cascade build run --real` now wires `RealExternalChecks`
  (real `cargo build --workspace` check) instead of the hardcoded
  `NoExternalChecks` stub, so `--real` runs actually gate ticket completion on
  a passing build. `--mock` and `--skip-externals` keep the no-op provider.

- **BudgetGuard fail-closed mode for autonomous runs** (CFC-10,
  T-P7-E13-08, 2026-08-17): the daemon's budget guard previously failed OPEN
  on undeterminable state — unknown account, missing rate windows, and
  unknown providers all returned `Allow`, so a dispatch could proceed with no
  budget information at all. `BudgetGuard` now has two constructors with
  identical limit arithmetic: `new` (fail-open — the historical behavior,
  unchanged, backing the interactive `budget_check` IPC method) and the new
  `new_fail_closed`, exposed on the IPC surface as a new
  `budget_check_autonomous` method for AUTONOMOUS dispatch paths (daemon
  scheduler, conductor fan-out). In fail-closed mode those unknown-state
  conditions return a new `BudgetResult::DenyUnknown` variant — deliberately
  distinct from `DenyLimit`/`DenyCost` so callers can tell "over budget" from
  "couldn't determine budget" — rendered on the wire as `allow: false` with
  the reason. A `quota-store.json` read failure in the autonomous method
  yields an empty store, which fail-closed reports as an unknown account and
  denies; the interactive method keeps its fail-open `Allow`. Explicitly
  disabled limits (`0`/`0.0`) still mean "check off" and allow in both modes,
  and interactive/manual budget checks are unchanged.

## [1.16.0] - 2026-07-12

### Removed
- `apps/cascade-widget-macos/` — duplicate SwiftPM scaffold that mirrored
  the canonical `src/widget/macos/` Xcode project; canonical builds use
  `src/widget/macos/build.sh` and the `CascadeWidget.xcodeproj` there.

## [1.15.2] - 2026-07-10

### Removed
- Dead `zai.rs` and `deepinfra.rs` adapter skeletons from
  `crates/cascade-providers/src/adapters/` (plus their test fixtures).
  Neither file was ever in the module tree (`adapters/mod.rs` declared
  neither `pub mod zai;` nor `pub mod deepinfra;`), so they never
  compiled and were silently rotting with stale types. **z.ai GLM is
  dispatched through Claude Code with the GLM endpoint env** (a GLM
  Coding Plan compliance requirement), not a direct API adapter — the
  `opencode` adapter already covers that path. DeepInfra was never
  provisioned.
- `cascade-daemon`: deleted dead `backup.rs` module and its `BackupConfig`
  struct. Backup functionality was never wired into the supervisor or any
  startup path; git history preserves the implementation for reference.
- `cascade-daemon`: deleted dead `backoff.rs` module and its `BackoffEntry`
  / `ACCOUNT_BACKOFF` static. Per-account exponential backoff has been
  superseded by the fleet routing layer in `cascade-core`; no call sites
  remained.
- `DaemonConfig::health_sample_interval_secs` and
  `DaemonConfig::event_bus_flush_interval_secs` fields removed.
  Both were parsed from config but never read by any runtime path; the
  health poller and event bus each use hard-coded intervals that reflect
  the actual validated behavior.
- `mcp_registration::register()` removed. MCP server self-registration
  was feature-flagged dead code; cascade no longer self-registers as an
  MCP server (the MCP server feature lives in the separate `cascade-mcp`
  crate).

### Changed
- `gemini::config::DEFAULT_MODEL` now references
  `MODEL_GEMINI_FLASH` (`gemini-3.5-flash`) instead of the retired
  `gemini-2.0-flash` literal.
- `gemini::adapter::available_models()` roster refreshed to the current
  GA/preview lineup: `gemini-3.1-pro`, `gemini-3.5-flash`,
  `gemini-3-flash` (per `models/models.yaml`).

## [1.15.1] - 2026-07-09

Chat reliability, fleet-first routing, UI polish, and the 2026-07 models audit.

### Changed
- **Conductor spill order is now fleet-first, A1-last**: codex → gemini →
  opencode → gfp → A2 → A1. Delegated work exhausts the separate-quota fleet
  (Codex/Gemini/OpenCode/GFP) before touching A1 (the interactive T0 account).
- **Cascade.app UI polish** — Fleet/Accounts/Chat visual redesign (quota bars,
  account badges, cards, chat input/messages, status badges).
- **models/ reference refreshed** (2026-07 audit): added github-copilot, devin,
  cursor-cli, zai-coding-plan; prefers gemini-flash-latest aliases (fixed IDs
  marked legacy); Z.ai flagship → GLM-5.2; use-case picks updated.

### Fixed
- **Chat never hangs** — per-chunk stall-guard (20s) errors instead of hanging
  when a provider stream opens then stalls.
- **Chat survives streaming congestion** — the Gemini adapter falls back to
  non-streaming `generateContent` when Google's `streamGenerateContent` 503s.


## [1.15.0] - 2026-07-08

The chat + accounts release: Cascade.app Personal chat works end-to-end, one
source of truth for fleet quota, and Phase C failover foundations.

### Added
- **In-app chat is live** — the daemon now spawns the `:9761` dashboard/chat
  server (`POST /api/chat`) that was never started before (the cause of the
  app's "Load failed" / "Disconnected"). Personal chat streams real replies.
- **3 read-only chat modes** (Personal / Cascade Setup / Project Questions)
  with strict per-mode system prompts: none edit code ("use Claude Code in the
  proper project directory"), and they say "I don't know" over guessing.
- **`GET /api/topics`** + a "view all topics" selector — lists the user's
  threads from `~/Downloads/.claude/threads` (shared personal workspace).
- **Personal-context loading** — Personal chat reads `~/Downloads/.claude`
  (memory + the focused thread) so it knows the user's topics.
- **Provider fallback chain** for chat — GF→GP→Codex→A2→A1→OC-Go: on a provider
  error it advances to the next available instead of failing.
- **Phase C session-failover foundations** (flag-gated `CASCADE_SESSION_FAILOVER`,
  off by default): session-copy, continuity handoff, backstop; `:3763` proxy is
  a documented deferred skeleton.

### Changed
- **quota.json is now a single source of truth** written solely by the daemon
  (atomic, on the poll interval) with a per-account `authenticated` field.
- Personal chat may use the fleet (Gemini/GPT) per user preference; only an
  explicit `vault`/`:private` namespace stays trusted-only (Anthropic/local).
- Chat GP-pool model → `gemini-flash-latest` (the pinned `gemini-2.0-flash`
  path 503'd).

### Fixed
- **App accounts display**: ×100 utilization bug (percentages now correct),
  false "Disconnected" (status derives from data freshness), and un-authed
  accounts are hidden with a "Re-auth" row.

### Ops
- Daemon binaries must be codesigned after a swap (unsigned → macOS
  `OS_REASON_CODESIGNING` kill); documented in the deploy recipe.


## [1.14.0] - 2026-07-07

Hardening release: richer rate-limit handling + dead-code cleanup. (Phase C
session-failover is deferred — see note below.)

### Added
- **Richer Anthropic 429 parsing** (`cascade-providers/http_client.rs`): when a
  bare `retry-after` header is absent, the client now derives the wait from
  Anthropic's `anthropic-ratelimit-requests-reset` / `-tokens-reset` headers
  (RFC3339 or bare seconds, soonest positive wait). Prefers explicit
  `retry-after` when present; fully defensive (malformed/absent → falls back,
  never panics). 6 new tests.

### Changed
- Cleaned up dead code surfaced while fixing clippy under `--features
  gemini-proxy`: removed an unused `StreamTranslator` token getter/field, a
  dead `GeminiProxy::with_upstream()`, and several unused re-exports (all
  verified zero-caller). `clippy --features gemini-proxy -D warnings` is now clean.
- Collapsed a redundant `map_model` branch (the `claude-sonnet` and default
  arms became identical after the v1.12.1 `gemini-flash-latest` change).

### Deferred
- **Session-failover (was planned for this slot)** is blocked: `~/.claude2`
  ("A2") is the *same* Anthropic account as A1 (identical account UUID/email;
  its session store symlinks A1's), so failing A1→A2 hits the same rate limit.
  It requires a genuinely separate Anthropic account before it can be built.

## [1.13.0] - 2026-07-07

Phase B of the vNEXT build-out: GFP circuit-breaker.

### Added
- **GFP circuit-breaker** — when the free Gemini pool is exhausted (all keys
  rate-limited), the `:3762` Anthropic-compat proxy returns an explicit 503
  with the key count, earliest-reset ETA, and fallback guidance, instead of a
  terse relay. It **never silently degrades into paid-Anthropic burn**.
  - `GpHealthSnapshot` gains `total_slots`, `earliest_reset_secs`,
    `is_exhausted()`; `:3761 /health` exposes `exhausted` + `earliest_reset_secs`.
  - `warn!` on entering true exhaustion (observable, not hidden).
  - `CASCADE_GFP_FALLBACK=agy` env flag (default unset = fail-loud). The agy
    (paid Gemini Pro) fallback is a documented seam that still fails loud today
    — agy needs real format translation, deferred to a future phase.
- 16 new tests covering exhaustion detection, fail-loud messaging, and flag routing.

## [1.12.1] - 2026-07-07

Phase A of the vNEXT build-out: make the `models/` reference load-bearing +
retirement-proof the GP proxy.

### Added
- **Runtime model-drift check** (`cascade-daemon/src/model_drift.rs`): the
  daemon embeds `models/models.yaml` at compile time (`include_str!`) and warns
  on boot if the live provider set drifts from the reference — so the models/
  directory is enforced at runtime, not just a doc.

### Changed
- **GP proxy retirement-proofing**: `anthropic_compat` now maps to
  `gemini-flash-latest` / `gemini-flash-lite-latest` (Google's auto-tracking
  aliases) instead of a pinned `gemini-2.0-flash` — immune to version
  retirements. `generateContent` serve confirmed against the aliases.
- Documented that C1's Codex plan tier is not recoverable from the local CLI
  (dashboard-only check).

### Fixed
- Committed the `conductor.rs` `AGY_MODEL_LABEL` → `MODEL_GEMINI_PRO` constant
  fix that the v1.12.0 commit inadvertently left out of its staged file set
  (the model-id gate is now clean against HEAD).

## [1.12.0] - 2026-07-07

Model-id doctrine made enforceable, with CI guards.

### Changed
- **Canonical model-id constants moved to the `cascade-types` leaf crate.**
  They lived in `cascade-core`, but `cascade-providers`/`cascade-mcp` depend
  only on `cascade-types` (not core), so they physically could not import the
  constants — which is why 11 sites hardcoded model-id string literals.
  Moving the 8 constants down to the leaf crate (with a re-export from
  `cascade-core` for compatibility) makes the "no hardcoded model-ids"
  doctrine actually enforceable. All 11 hardcode sites now use the constants.

### CI
- **`scripts/check-model-ids.sh`** — fails the build if any current canonical
  model-id literal is hardcoded outside the doctrine file (tight match on the
  8 exact values; no false positives on provider ids or GP-proxy targets).
- **`scripts/check-models-consistency.sh`** — fails if a `model_ids.rs`
  constant is missing from `models/models.yaml`; warns (not fails) if
  `models.yaml` is >90 days stale (staleness detector).
- Both wired into `.github/workflows/model-id-gate.yml`.

## [1.11.0] - 2026-07-07

Disk Guardian + the canonical model reference directory.

### Added
- **Disk Guardian** — a daemon watchdog (sibling to the RAM Guardian) that
  samples free space on the scratch volume every 30s and reaps stray
  ephemeral build artifacts under the scratch root when free space drops
  below threshold (2 GiB / 5% Critical). Targets exactly what fills the
  boot volume during heavy agent work: isolated cargo target dirs,
  `*claude-worktrees*`, `*-test-target`, and nested `target/` dirs.
  Conservative multi-gating (Warn/Critical status + under scratch root +
  ≥30 min old + known-ephemeral pattern; never the real repo target,
  `$HOME`, or a bare-root `target`). `DISK_GUARDIAN_DISABLE=1` = log-only;
  `DISK_GUARDIAN_SCRATCH_ROOT` overrides the default temp dir. 27 tests.
- **`models/` reference directory** — an agent-readable model×subscription
  matrix (README.md + machine-readable `models.yaml` + per-provider files)
  so Cascade agents and the daemon selection can look up which subscription
  has which model and what each is best for, with current (July 2026) info.

### CI
- Linux CI jobs moved off billing-blocked GitHub-hosted runners onto the
  self-hosted `cam-sentry` runner (48 jobs across 16 workflows).

## [1.10.1] - 2026-07-06

Security + correctness fixes from a deep repo audit.

### Security
- **Conductor path sensitivity firewall** — the delegation path now enforces
  the same protected-content firewall as the chat path. `cascade-core`'s
  `select_target_for_prompt` classifies prompt content and, when Sensitive
  (PII / VA / health / personal), excludes untrusted providers
  (gemini / gfp / codex / opencode), failing closed if no trusted lane
  remains; the CLI dispatch loop applies a reactive backstop that spills a
  sensitive prompt off an untrusted lane to a trusted Claude lane. Closes a
  gap where conductor fan-out could send sensitive content to an external
  provider.

### Fixed
- **GCI file ops are real** — `gci_write_file` / `gci_delete_file` now perform
  atomic filesystem writes/deletes through a canonicalize-based path guard
  (base `~/.claude`, rejects `..` traversal and absolute escapes) instead of
  returning fake `{"written":true}` without touching disk.
- **Scheduler persists** — the automation scheduler uses a persistent
  `scheduler.db` (was `:memory:`, lost on restart); removed two no-op
  `"true"` seed tasks.
- **FleetPoller honesty** — inert ClaudeMax/Codex/Agy quota sources now carry
  accurate docs pointing at the real data flow rather than silently
  returning `None`.
- **Dead fake-crypto labeled** — the unused XOR placeholder HMAC/SHA256 in
  `security/oauth.rs` is renamed with loud "NOT REAL CRYPTO" warnings (real
  PKCE lives in `cascade-providers/src/oauth`).

## [1.10.0] - 2026-07-06

Gemini Pro conductor lane (via the owner's paid Google AI Pro subscription).

### Added
- **Gemini Pro dispatch lane** — `cascade conductor --account gemini-acc1`
  now dispatches real completions through the owner's paid Google AI Pro
  (Google One) subscription via the agy / `cloudcode-pa` path, using the
  existing `~/.cascade/agy-token.json` (OAuth refresh, no new browser flow).
  Model label `gemini-3.1-pro` (the cloudcode-pa `model` field is
  `gemini-pro-agent`). Gated on token presence — falls back to `Unavailable`
  (spill chain intact) when absent. Owner-authorized for personal use of a
  subscription they pay for. Live-verified end-to-end.
- `.github/docs/MODEL-MATRIX.md` — model × subscription reference (verified
  July 2026), including the per-project Gemini free-tier quota facts.

### Fixed
- GFP conductor lane no longer hardcodes a stale `claude-sonnet-4-6` model
  string — uses the canonical `MODEL_CLAUDE_SONNET` (the :3762 proxy remaps
  the `claude-sonnet` prefix to the free Gemini Flash tier).

## [1.9.22] - 2026-07-04

Update-apply handover fix and workspace-wide lint parity.

### Fixed
- **launchd-aware self-restart**: `cascade update apply` under launchd
  supervision no longer leaves the old daemon alive while spawning an
  orphaned child (port fight on :3761/:3762 that took the proxy down until
  a manual bootout). When launchd-supervised the daemon now exits cleanly
  and lets KeepAlive respawn the swapped binary.
- Resume-subprocess tests serialized and exec-wrapped, fixing an orphan
  process race.
- **nSentry CI bridge no longer backfills stale failures**: the GitHub-Actions
  poller (`gh-ci-failures-to-reports.sh`, embedded in the daemon) gained a
  `--max-age-days` cutoff (default 3, `NSENTRY_MAX_AGE_DAYS`). Previously a
  repo's first-ever scan — or a freshly-created `.gh-seen` after a redeploy —
  would alert on every historical failure `gh run list` returned, however old
  (a year-old acamarata/ali run fired as new on 2026-07-06). Runs older than
  the cutoff are now skipped even if unseen.
- Workspace-wide `cargo clippy --all-targets -D warnings` is clean across
  every crate and the Tauri app (CI-parity for when Actions billing is
  restored): bin-tree test_support declarations, redundant test module
  wrappers removed, env-lock await allows documented, unused imports, doc
  formatting, and a temp-dir race in an app test.

## [1.9.21] - 2026-07-03

GP pre/post middleware (flag-gated, all OFF by default) and a private-chat
provider firewall.

### Added
- **GP pre-middleware** (`[middleware]` config, every flag defaults OFF —
  zero hot-path cost when disabled): context compression (GP-summarize old
  turns past a token threshold, keep recent turns verbatim), byte-stable
  system-prompt injection from `~/.cascade/CASCADE.md` (prompt-cache-safe),
  and bounded request classification that may only DOWNGRADE the model
  (Gemini family only). Every middleware failure falls back to the original
  request unchanged.
- **Context-sync post-middleware** (`middleware.context_sync`, default OFF):
  background response digests to `~/.cascade/context-sync/` JSONL with a
  RAG-watcher index nudge; never blocks or delays the chat response.
- `ProviderRegistry::pick_for_chat_filtered` — provider selection with a
  candidate filter; explicit selection bypasses by contract, fail-closed
  when everything is filtered out.
- `registry_provider_is_trusted_for_sensitive` — deny-by-default trust
  classifier for daemon adapter ids (trusted: Anthropic, Claude accounts,
  local models).

### Security
- **Private-chat provider firewall**: protected namespaces (`personal`,
  `personal:private`, `*:private`) can no longer reach the GP pool or any
  untrusted provider on the default path — the app skips the :3762
  fast-path, the daemon skips GP-first steering AND constrains fallback
  provider selection to trusted providers, failing closed with an honest
  error when none is registered. An explicitly user-pinned provider still
  wins (deliberate choice).
- Protected namespaces neutralise content-capturing middleware
  (context-sync digests, GP compression, GP classification) so private
  chat never feeds the global RAG index or leaves via Google side
  channels; local-only prompt injection stays available.
- **:3762 proxy server-side firewall**: the Anthropic-compat GP proxy now
  refuses protected-namespace requests (403) itself, so a non-app
  localhost caller cannot bypass the app-side gate. One canonical
  namespace classifier now lives in cascade-core, shared by the chat
  handler, the proxy, and mirrored in the app.
- **Never-resurrect applies to loading**: a curated providers.json with
  zero gemini-free entries now blocks the keychain/env key fallbacks at
  boot (previously revoked keys were still used in memory for the current
  boot; only the write-back respected revocation).

### Fixed
- The auto-detected `ollama` adapter is trusted for private chat (it is
  local in fact); previously a private chat with only Ollama available
  would over-refuse.
- Daemon bin test target compiles again (missing `test_support`
  declaration); IPC integration tests updated to the 4-arg
  `IpcServer::new`; env-mutating key_loader tests serialized under the
  shared env lock.
- `cascade-core` clippy: MutexGuard-held-across-await in the env allowlist
  and integration tests; misc test lints. Daemon test hygiene (dead helper,
  unused imports).
- Packaging workflows (Homebrew/Scoop/Chocolatey) download release assets
  with authenticated `gh release download` — required for private-repo
  assets.

## [1.9.20] - 2026-07-02

Routing unified, overnight continuity, RAM guardian, and adversarial-review hardening.

### Added
- **Unified selection module** — one quota-aware spill brain now drives the CLI Conductor, the TaskClass matrix router, and the daemon chat picker (previously three unrelated systems). T3/cheap work prefers the free Gemini pool when it is actually healthy (live routing-table signal, not config), the GFP delegate lane executes through the local pool proxy for real, and a `gp-pool` chat adapter registers at boot so chat's pool-first behavior genuinely engages. `CASCADE_GFP_LANE_OFF` disables the lane.
- **Cascade Continuity** (`cascade continuity add|list|rm|status`) — disk-persisted continuation intents; the daemon watches account reset times and fires a bounded headless resume (or a notification) the moment a capped account's window reopens. Idempotent, crash-safe, one shot per intent.
- **RAM Guardian** (`cascade ram status|sweep`) — daemon memory watchdog with a conservatively quadruple-gated stray-process reaper (only reparented, old, known build/test binaries, and only under memory pressure; never the daemon or any interactive app). `RAM_GUARDIAN_DISABLE=1` makes it log-only.

### Fixed (adversarial review)
- `providers.json` — which now holds pool API keys as the source of truth — is written `0600` and tightened on boot if found looser (was world-readable).
- Key-import fallback is value-deduplicated and respects intentional key removal — repeated boots can no longer grow the store or resurrect revoked keys.
- The Anthropic-compat proxy's upstream client has bounded connect/overall timeouts — a stalled upstream can no longer hold client connections open forever.

## [1.9.19] - 2026-07-02

`cascade update` works end to end, chat privacy is consent-gated, and the GP pool reports only real keys.

### Added
- **`cascade update` is functional.** The daemon now registers the `update_check` / `update_apply` / `update_auto` IPC handlers (they existed CLI-side only). `check` compares the running version against the latest GitHub release; `apply` downloads the release bundle (`cascade-v{version}-macos-aarch64.tar.gz`), verifies it against the release's `SHA256SUMS` (hard-fails on mismatch — never installs unverified bytes), snapshots the old binaries for rollback, swaps atomically, and restarts the daemon; `auto` toggles auto-update in config. Releases now ship the bundle + checksums as assets, so updating is one command from here on.

### Fixed
- **Personal chat persistence is consent-gated.** An adversarial review found the app self-asserted the personal-namespace `opt_in` whenever the Personal tab was active, silently persisting chats server-side with no user decision. Persistence is now off by default and gated behind a real setting (`memory.personalChatSync`); when off, Personal history stays on-device and the UI says so.
- **"Clear chat" clears server-side history too** — it previously wiped only localStorage while daemon-persisted turns silently survived. A DELETE endpoint now purges the scope+namespace and the UI calls it.
- **Private-mode copy is honest** — it now states that private messages skip history and memory but are still processed by the selected model provider.
- **GP pool lists only real keys** — invalid vault entries are no longer counted or given proxy slots, and an IP-restricted key is kept but disabled locally. Pool errors now surface Google's actual reason (rate-limit, IP restriction) instead of a generic exhaustion message.

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

- The `bge-m3` compatibility mode uses nearest fastembed equivalents until
  upstream ships native BGE-M3; swap is config-only.
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

[Unreleased]: https://github.com/acamarata/cascade/compare/v1.15.2...HEAD
[1.15.2]: https://github.com/acamarata/cascade/compare/v1.15.1...v1.15.2
[1.15.1]: https://github.com/acamarata/cascade/compare/v1.15.0...v1.15.1
[1.15.0]: https://github.com/acamarata/cascade/releases/tag/v1.15.0
[0.1.0]: https://github.com/acamarata/cascade/releases/tag/v0.1.0
