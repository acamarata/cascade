# Cascade — MASTER-FEATURES.md

**Purpose:** Single source of truth for the complete feature surface across all Cascade product pillars. Used by all agents during Plan and Build phases. Cross-references phase/epic for every feature. Read before planning new work; update immediately when a feature ships.

**Status legend:**
- ✅ Done — production-ready, tested, shipped
- 🟡 Partial — code written but incomplete or untested
- 🔲 Planned — forged in a phase, has tickets
- ➕ New — identified in product vision, not yet in any forged phase
- 🚫 Deferred — explicitly deferred to P5 or ClawDE fork

**Source of truth:** `.opencode/phases/sport/MASTER-FEATURES.md`
**Last updated:** 2026-06-01
**Contributing phases:** P2 (ready\_to\_build), P3 (planning, 7 epics), P4 (planning, 6 epics), P5 (unplanned), ClawDE fork (future rewrite)

---

## 1. Identity & Principles

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-IDENTITY-FOSS | FOSS, MIT license | All Cascade code MIT; no telemetry; user owns data; public GitHub repo + public CI establish that the FOSS public launch is complete (the old `v0.1.0` wording is not a pending milestone) | ✅ Done | Architecture + T-P7-E23-06 |
| F-IDENTITY-FILE-BASED | File-based, no server DB | All state in `.cascade/` flat files; SQLite for search only; no Postgres | ✅ Done | Architecture |
| F-IDENTITY-PLUGIN | Plugin-extensible via WASM | Third-party data sources, tools, widget components via WASM sandbox | 🔲 Planned | P4-E03 |
| F-IDENTITY-HARNESS-AGNOSTIC | Harness-agnostic core | Core cascade resolution works identically with CC, OC, Codex, and any future harness | 🔲 Planned | P4-E02 |
| F-IDENTITY-META-HARNESS | Augmenting meta-harness | Cascade augments other harnesses; does not replace their execution loops | ✅ Done | Architecture |
| F-IDENTITY-FORK-LIGHT | Fork-friendly clean code | No DB abstraction layer over-engineering; native DB + nSelf-sync version is a future ClawDE rewrite | ✅ Done | Architecture |
| F-IDENTITY-CLAWDE-FUTURE | ClawDE fork (future) | Native DB + nSelf-sync rewrite of Cascade; out of scope for P2–P4 | 🚫 Deferred | ClawDE fork |
| F-IDENTITY-SELF-HOST | Cascade dogfoods itself | Repo self-hosts its own PRC at `.cascade/CASCADE.md` | ✅ Done | T-P4-E05-19 |

---

## 2. Instruction Cascade (6-tier GCI/PCI/APC/PPC/PRC/PAC)

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-CASCADE-6TIER | 6-tier cascade model | GCI (global) → PCI (personal) → APC (all-projects) → PPC (per-project) → PRC (per-repo) → PAC (per-app) | 🔲 Planned | P2-E02 |
| F-CASCADE-RESOLVE | Cascade resolution engine | Rust `cascade-core` crate: load + merge all 6 tiers, higher tier wins on conflict | 🔲 Planned | P2-E02 |
| F-CASCADE-DEFAULTS | Tier default locations | GCI: `~/.cascade`; PCI: `~/Downloads/.cascade`; APC: `~/Sites/.cascade`; PPC: `{project}/.cascade`; PRC: `{repo}/.cascade`; PAC: `{app}/.cascade` | 🔲 Planned | P2-E02 |
| F-CASCADE-PPCI-INBOX | PPCi inbox | Per-project cascade inbox (PPCi) for cross-project messaging, distinct from PCI tier name | 🔲 Planned | P2-E02 |
| F-CASCADE-GENERATE-HARNESS | Harness file generation | Auto-generate CLAUDE.md / AGENTS.md / Codex config at each tier pointing back at `.cascade/` source | 🔲 Planned | P4-E02 |
| F-CASCADE-TEMPLATES | Cascade templates | Vendor-neutral GCI/PCI/APC/PPC/PRC/PAC default templates + 16 stack templates + 11 project-shape templates | 🔲 Planned | P3-E05 |
| F-CASCADE-TEMPLATE-APPLY | Template apply + diff + upgrade | `cascade template apply/diff/upgrade`; GUI browser; template inheritance with override semantics | 🔲 Planned | P3-E05 |
| F-CASCADE-TEMPLATE-AUTHOR | Custom template authoring | `cascade template create/validate/export`; power users package + share templates | 🔲 Planned | P3-E05 |
| F-CASCADE-SYMLINKS | Per-tool symlink management | `cascade link/unlink --tool <name>`; sibling symlinks so CC/OC/Codex read cascade content transparently | 🔲 Planned | P3-E03 |
| F-CASCADE-RESTORE | Archive restore primitives | `cascade restore --tool <name>`; fully reversible; never deletes, only moves to `.cascade/legacy/` | 🔲 Planned | P3-E03 |

---

## 3. Knowledge & Memory (Obsidian-class, file-based)

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-KNOW-VAULT | Markdown knowledge vault | Flat `.cascade/` directory as the canonical vault; all content in plain Markdown | 🔲 Planned | P2-E02 |
| F-KNOW-WIKILINKS | Wikilinks + backlinks | `[[wikilink]]` resolution across vault files; backlink index | 🔲 Planned | P3-E06 / P4-E01 |
| F-KNOW-GRAPH-VIEW | Graph view of vault | Visual link graph of all vault files and their relationships | 🔲 Planned | P3-E06 + P4-E01 |
| F-KNOW-TAGS | Tags + filtering | `#tag` support in cascade files; tag-based filtering in dashboard | 🔲 Planned | P3-E06 |
| F-KNOW-FTS | Full-text search | ripgrep-backed full-text search across all cascade tiers | 🔲 Planned | P4-E01 |
| F-KNOW-MEMORY-DECISIONS | Memory: decisions | `memory/decisions.md` — significant technical choices with rationale | 🔲 Planned | P2-E02 |
| F-KNOW-MEMORY-LESSONS | Memory: lessons | `memory/lessons.md` — gotchas and mistakes to avoid repeating | 🔲 Planned | P2-E02 |
| F-KNOW-MEMORY-PATTERNS | Memory: patterns | `memory/patterns.md` — established codebase conventions | 🔲 Planned | P2-E02 |
| F-KNOW-THREADS | Threads viewer | Memory threads listed and browsable in dashboard | 🔲 Planned | P3-E02 |
| F-KNOW-IDEAS | Ideas inbox | `ideas/` directory; captured from any source; dashboard inbox view | 🔲 Planned | P3-E02 |
| F-KNOW-PROMPT-LIBRARY | Prompt / agent / persona library | Author, store, version, and inject named prompts/agents/personas into any connected harness | 🔲 Planned | P3-E07 / P4-E02 |
| F-KNOW-CONTEXT-CURATION | Context curation UI | Pin and build a session context from vault content; send to harness | 🔲 Planned | P3-E07 |
| F-KNOW-CRD-CHAINS | CRD chain viewer | Claude Relay Daemon chain list visible in personal panel | 🔲 Planned | P3-E02 |
| F-KNOW-SCHEDULED-TASKS | Scheduled task viewer | List scheduled tasks from dashboard; daemon executes them | 🔲 Planned | P2-E02 + P3-E02 |

---

## 4. Fleet, Quota & Gemini Pool

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-FLEET-MULTI-ACCOUNT | Multi-account tracking | Track quota/usage across Anthropic acct1–acct4 + OpenAI Codex C1 + Gemini G1–G4 | 🔲 Planned | P2-E02 + P3-E02 |
| F-FLEET-QUOTA-LIVE | Live quota display | Per-account utilization + renewal countdown; near-real-time | 🔲 Planned | P2-E02 |
| F-FLEET-QUOTA-HISTORY | Historical cost/usage analytics | Weekly/historical bar charts, per-account ledger drilldown, date-range picker | 🔲 Planned | P3-E02 |
| F-FLEET-LEDGER | Per-account usage ledger | Token counts + cost estimates per provider call; stored by daemon | 🔲 Planned | P2-E02 + P3-E02 |
| F-FLEET-GEMINI-POOL | Central Gemini Pool proxy | Daemon-held proxy at localhost:3761; 28 free-tier Gemini keys; round-robin + 429-retry | 🔲 Planned | P2-E02 |
| F-FLEET-ANTHROPIC-FAILOVER | Anthropic request-level failover proxy | Loopback :3763 Anthropic Messages endpoint; selects account per request (select_account spill order), shells `claude -p --output-format stream-json`, re-frames as Anthropic SSE, spills to next account on captured 429/auth signatures; opt-in activation via ANTHROPIC_BASE_URL | ✅ Done (P7-E13-09) | P7-E13 |
| F-FLEET-OC-ROUTING | OC multi-model routing | OC routes GPT/Gemini/DeepSeek through the Gemini Pool OpenAI-compatible proxy | 🔲 Planned | P4-E02 |
| F-FLEET-GCP-PROVISION | Assisted GCP provisioning | Wizard-guided: per-account Google OAuth → GCP project → Gemini API key creation → add to Pool | 🔲 Planned | P3-E04 |
| F-FLEET-PROVIDER-PROVISION | Assisted provider provisioning | Same assist for Anthropic/OpenAI/Codex; permission-gated; keys encrypted at rest via OS keychain | 🔲 Planned | P3-E04 |
| F-FLEET-WIDGET | Embedded fleet widget | Fleet usage widget (all models/accounts) embedded in Cascade.app sidebar | 🔲 Planned | P3-E01 |

---

## 5. Daemon, CLI & Harness Bridge

### 5a. Daemon (`cascaded`)

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-DAEMON-TOKIO | Tokio async runtime | Production-quality Tokio event loop; per-OS supervised restart | 🔲 Planned | P2-E02 |
| F-DAEMON-CONFIG | Config parser | `~/.cascade/config.toml` via TOML + serde with validation and defaults | 🔲 Planned | P2-E02 |
| F-DAEMON-FILEWATCHER | File watcher | notify-rs watcher with 200ms debounce + derived-file auto-regeneration | 🔲 Planned | P2-E02 |
| F-DAEMON-SQLITE | SQLite state persistence | `events.db` WAL mode; event bus; quota-poll + project-state-poller as async tasks | 🔲 Planned | P2-E02 |
| F-DAEMON-HEALTHCHECK | Healthcheck snapshot | PID, uptime, queue\_depth, ram\_kb, cpu\_pct, index\_freshness — feeds CLI + widgets | 🔲 Planned | P2-E02 |
| F-DAEMON-SUPERVISOR | Per-OS supervision | LaunchAgent (macOS), systemd user unit (Linux), Windows Service; exponential backoff | 🔲 Planned | P2-E02 |
| F-DAEMON-BACKUP | `.cascade/` backup sync | Scheduled mirror of `.cascade/` directory to backup path | 🔲 Planned | P2-E02 |
| F-DAEMON-HOOKS | Hooks runner | Execute user-configured hooks on daemon events; authoring UI in dashboard | 🔲 Planned | P2-E02 + P3-E02 |
| F-DAEMON-SCHEDULER | Scheduled-task executor | Daemon runs cron/launchd jobs (not just displays them); task result storage | 🔲 Planned | P2-E02 |
| F-DAEMON-STATUS-CACHE | Status cache file | `~/.cascade/cache.json` schema v1 written by daemon; consumed by all widgets | 🔲 Planned | P2-E02 |
| F-DAEMON-AUDIT-LOG | Append-only audit log | JSONL at `~/.cascade/audit.log` (0600); chain integrity via SHA-256 chaining | 🔲 Planned | P2-E07 |
| F-DAEMON-KEYCHAIN | OS keychain integration | `cascade-keychain` crate: macOS Security, Linux Secret Service, Windows Credential Manager | 🔲 Planned | P2-E07 |
| F-DAEMON-LOCAL-TOOL-INVOKER | LocalToolInvoker for core tools | `ToolInvoker` impl for file read/write, bash exec, grep/glob behind the sensitivity firewall + capability gating | ✅ Done | T-P7-E04-01 |
| F-DAEMON-LOCAL-TOOL-INVOKER-WIRED | Wire into executor, remove duplicate FallbackInvoker | `CeoRuntime::build_executor` (`ipc_ceo.rs`) now dispatches through `SafetyGate<LocalToolInvoker>`; deleted the duplicate fake `FallbackInvoker` so there is one real tool-dispatch path regardless of `ProviderRegistry` wiring | ✅ Done | T-P7-E04-02 |
| F-BUILD-REAL-EXTERNAL-CHECKS | Wire RealExternalChecks into cascade build run | `cascade build run --real` gates on real build/test checks (RealExternalChecks) instead of NoExternalChecks; `--mock`/`--skip-externals` keep the no-op provider | ✅ Done | T-P7-E03-02 |
| F-BUILD-REAL-FLAG | Add --real flag to cascade build run | `cascade build run <phase>` requires an explicit `--real` (FleetDispatcher) or `--mock` (MockDispatcher) flag; no-flag case gives a clear, non-refusing error explaining both options instead of hard-refusing | ✅ Done | T-P7-E03-04 |

### 5b. CLI (`cascade`)

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-CLI-STATUS | `cascade status` | Daemon PID, uptime, index freshness, MCP port, queue depth | 🔲 Planned | P2-E03 |
| F-CLI-RESOLVE | `cascade resolve` | Print resolved cascade for CWD | 🔲 Planned | P2-E03 |
| F-CLI-SEARCH | `cascade search <query>` | RAG search via daemon | 🔲 Planned | P2-E03 + P4-E01 |
| F-CLI-INBOX | `cascade inbox list/send` | List and send PPCi inbox messages | 🔲 Planned | P2-E03 |
| F-CLI-MEMORY | `cascade memory read/write` | Read/write memory files via daemon | 🔲 Planned | P2-E03 |
| F-CLI-CONFIG | `cascade config get/set` | Manage cascade settings | 🔲 Planned | P2-E03 |
| F-CLI-LINK | `cascade link/unlink` | Add/remove per-tool symlinks | 🔲 Planned | P2-E03 |
| F-CLI-MIGRATE | `cascade migrate` | Migrate legacy `.claude/`, `.opencode/` content to cascade | 🔲 Planned | P3-E03 |
| F-CLI-DOCTOR | `cascade doctor` | Diagnose broken symlinks, missing deps, config issues | 🔲 Planned | P2-E03 |
| F-CLI-DAEMON | `cascade daemon start/stop/restart` | Daemon control | 🔲 Planned | P2-E03 |
| F-CLI-TEMPLATE | `cascade template list/apply/diff/upgrade/create` | Full template lifecycle | 🔲 Planned | P3-E05 |
| F-CLI-COMPLETIONS | Shell completions | bash/zsh/fish/PowerShell completions via clap\_complete | 🔲 Planned | P2-E03 |
| F-CLI-INIT | `cascade init [tier]` | Initialize a cascade at the given tier | 🔲 Planned | P2-E03 |
| F-CLI-ROLLBACK | `cascade rollback` | Signed delta rollback for RAG index updates | 🔲 Planned | P4-E04 |
| F-CLI-PLUGIN | `cascade plugin new/test/install/remove` | Plugin lifecycle management | 🔲 Planned | P4-E03 |

### 5c. IPC

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-IPC-JSONRPC | JSON-RPC 2.0 socket | Unix domain socket (macOS/Linux) + Named Pipe (Windows); length-prefixed framing | 🔲 Planned | P2-E03 |
| F-IPC-PROTOCOL | Protocol-version field | Future-proof versioning so client/server can detect mismatches | 🔲 Planned | P2-E03 |
| F-IPC-SCHEMA-VALIDATION | IPC schema validation | All inbound IPC messages deserialized through `cascade-types` schema registry with `deny_unknown_fields` | 🔲 Planned | P2-E07 |
| F-IPC-STATUS-BROADCAST | Status broadcast | Daemon broadcasts status updates to all widget subscribers | 🔲 Planned | P2-E02 |

### 5d. Harness Bridge (CC + OC + Codex)

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-BRIDGE-CC | CC integration | Generate CLAUDE.md at each cascade tier; configure MCP in `~/.claude/settings.json` | 🔲 Planned | P4-E02 |
| F-BRIDGE-OC | OC deeper integration | Drive/monitor OC; generate AGENTS.md; configure `opencode.json`; deeper open-source hooks | 🔲 Planned | P4-E02 |
| F-BRIDGE-CODEX | Codex integration | Generate Codex config at each tier; integrate into harness bridge | 🔲 Planned | P4-E02 |
| F-BRIDGE-HARNESS-STATUS | Harness status panel | CC/OC/Codex detection + instruction-file link status in dashboard | 🔲 Planned | P3-E02 |
| F-BRIDGE-REGEN | Harness instruction regeneration | `POST /api/gci/harness-regenerate`; updates CLAUDE.md/AGENTS.md symlinks at each tier | 🔲 Planned | P3-E02 |
| F-BRIDGE-CROSS-REPO | Active cross-repo dispatch | Cascade triggers and monitors CC/OC tasks across repos | 🔲 Planned | P4-E02 |
| F-BRIDGE-CONTEXT-OPTIMIZE | Token/context optimization | rtk-equivalent: compress and optimize context Cascade serves to harnesses | 🔲 Planned | P4-E04 |

---

## 6. Tauri Desktop App + Widgets

### 6a. Cascade.app (Tauri 2)

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-APP-SHELL | Tauri 2 app shell | Native desktop app: React+Vite+TS+Tailwind+shadcn/ui; macOS/Linux/Windows | 🔲 Planned | P3-E01 |
| F-APP-IPC-BRIDGE | Tauri IPC bridge | Wraps P2/E03 JSON-RPC daemon contract; Tauri commands frontend API | 🔲 Planned | P3-E01 |
| F-APP-ROUTING | SPA routing | React Router v6 with window/panel navigation | 🔲 Planned | P3-E01 |
| F-APP-COMMAND-PALETTE | Command palette | Cmd+K / Ctrl+K fuzzy search across all actions | 🔲 Planned | P3-E01 |
| F-APP-THEME | Theme system | Dark / light / system-follow with CSS variables | 🔲 Planned | P3-E01 |
| F-APP-ACCESSIBILITY | WCAG 2.1 AA | aria labels, roles, contrast ≥4.5:1, focus rings, keyboard nav | 🔲 Planned | P3-E01 |
| F-APP-MULTIWINDOW | Multi-window support | Open/focus/close secondary windows via Tauri | 🔲 Planned | P3-E01 |
| F-APP-KNOWLEDGE-VAULT | Knowledge vault UI | Markdown vault browser (Obsidian-class): files, links, tags, full-text search | 🔲 Planned | P3-E06 + P4-E01 |
| F-APP-MEMORY-VIEWER | Memory & threads viewer | Browse decisions/lessons/patterns/threads from vault | 🔲 Planned | P3-E06 |
| F-APP-PROJECT-MAP | Project map graph view | Visual `.cascade` tier cascade diagram + PEWS phase DAG | 🔲 Planned | P3-E07 |
| F-APP-AGENTS-PERSONAS | Agents/personas manager | Create, edit, version, and inject agent/persona configs into harnesses | 🔲 Planned | P3-E07 |
| F-APP-CONTROLS | Daemon/harness controls | Start/stop/restart daemon; harness connection toggles; MCP admin | 🔲 Planned | P3-E01 + P3-E02 |
| F-APP-GP-CHAT | GP Chat panel | Floating bottom-right chat streaming completions via Gemini Pool; tool catalog; markdown rendering; history | 🔲 Planned | P3-E02 |
| F-APP-CONTEXT-CURATION | Context curation panel | Pin and assemble session context from vault; export to harness | 🔲 Planned | P3-E07 |
| F-APP-RAG-EXPLORER | RAG explorer panel | Browse and query local RAG index; drag-and-drop ingest; tier enable/disable | 🔲 Planned | P4-E01 |
| F-APP-PROVIDER-SETTINGS | Provider settings page | List/add/remove/test AI providers; connection status; model routing table | 🔲 Planned | P3-E04 |
| F-APP-SETTINGS | Settings: configure everything | Full settings parity: Gemini Pool, provider keys/OAuth, harness bridges, `.cascade` tiers, hooks, scheduled tasks, plugins, vault, MCP servers | 🔲 Planned | P3-E07 |
| F-APP-PAID-MODEL-SAFETY-COVERAGE | Verify paid-model-safety UX switches coverage | 🟡 Gap confirmed: `ContextPrivacyTab.tsx` has no master “Disable all paid models” switch, free-only mode, or GCP billing wizard/entry point, and the settings/runtime schema has no paid-safety enforcement wiring. Fix requires cross-cutting schema, routing enforcement, and billing-wizard work beyond this S-sized audit. | 🟡 Partial | T-P7-E23-05 |
| F-CLI-LINK-RESTORE-AUDIT | Verify cascade link/restore + symlink-bridge coverage | Code audit complete. `cascade link --tool` is real for all five tools; `cascade restore --tool` is fully implemented (manifest-based, atomic, HOME-confined, tested). One bug fixed: aider was mapped to `.aider.conf.yml` (the config file) instead of `.aider.md` (the instruction file). Fixed in `cmd/link.rs`. `create_siblings` in cascade-core creates only CLAUDE.md + AGENTS.md on init (not .cursorrules/.aider.md); spec says all four should be auto-created — logged as a follow-up (larger change, not S-weight). | ✅ Done | T-P7-E23-03 |
| F-CLI-LINK-TIER1-CONFLATION | Clarify Tier-1 symlink-bridge vs provider-adapter | Determination complete. Two completely separate systems: (1) **Symlink-bridge** (`cmd/link.rs`, `cascade-core/symlinks.rs`, `cmd/init.rs`) — creates filesystem symlinks inside `.cascade/` (CLAUDE.md/AGENTS.md/…→CASCADE.md) so tools read cascade instructions transparently; no inference involved. (2) **Provider-adapters** (`cascade-providers/src/adapters/`) — implement `ProviderAdapter` for AI inference routing; Cursor/Antigravity are detect+config-generation only (ToS, no inference), OpenCode/Anthropic/etc. are real inference adapters. doc-06's Tier-1 description (“CLAUDE.md/AGENTS.md symlink-bridge + OpenCode mode-template installer”) refers exclusively to system (1). The adapters directory is system (2). No conflation in the code — only a gap in doc-06 which does not acknowledge the separate adapter-based MCP config-generation path for Cursor/Antigravity. | ✅ Done | T-P7-E23-04 |
| F-PROV-CURSOR-SCOPE-HONESTY | Cursor adapter honest detect+config scope | Honest relabel chosen over implementing routing (product-boundary lock: Cursor must not become a full subscription adapter). Removed the unreachable `#[cfg(feature = "inference_routing")]` stub block in `adapters/cursor.rs` ("stub — not implemented"), removed the orphaned `inference_routing` Cargo feature, made `complete`/`complete_stream` unconditionally return an accurate `UnsupportedTaskType` message, and `available_models` return an empty list (trait contract feeds the routing model-picker; nothing is routable through this adapter) | ✅ Done | T-P7-E12-01 |
| F-PROV-ANTIGRAVITY-SCOPE-HONESTY | Antigravity adapter honest detect+config scope | Same disposition as T-P7-E12-01 for consistency: removed the identical cfg-stub block in `adapters/antigravity.rs`, unconditional accurate `UnsupportedTaskType`, `available_models` returns empty (dropped the fabricated `antigravity-subscription` placeholder id). Module doc now disambiguates this detection adapter from the real `agy`-CLI Gemini dispatch in `cascade-cli` conductor (`Provider::Gemini`/`execute_gemini`), which was not touched | ✅ Done | T-P7-E12-02 |
| F-PROV-ZAI-DEEPINFRA-OBSOLETE | z.ai/DeepInfra named adapter items closed as obsolete | Verification-only closure: `zai.rs`/`deepinfra.rs` were removed as dead code in v1.15.2 and are intentionally NOT rebuilt — z.ai GLM dispatch already runs for real through the Claude-Code path (`conductor.rs:481` `Provider::Zai => execute_claude` + GLM endpoint env). Zero code references remain; only pre-removal planning docs mention them. Closure note recorded in `.claude/phases/current/p7/residue.md` | ✅ Done | T-P7-E12-03 |
| F-COND-MIDRUN-QUOTA-REREAD | Conductor mid-run quota re-check between spill attempts (CFC-05) | `execute_with_fallback` (cascade-cli conductor.rs) re-reads quota.json via `refresh_snapshot_preserving_tried` before each spill-target selection, so a lane saturated mid-run by another process (daemon utilization update, concurrent conductor run) is skipped instead of dispatched. Best-effort: missing file / torn JSON falls back to the in-memory startup snapshot (fallible `try_load_quota_snapshot_from` distinguishes "failed re-read" from "no accounts"). Typed tried-lane bookkeeping (T-P7-E13-02) re-applied over the fresh snapshot for ALL tried lanes — also fixes premature spill exhaustion after 2+ failures (old code re-marked only the latest failure). Spill order, sensitivity firewall, and fan-out executor untouched. 3 new tests incl. stale-snapshot control | ✅ Done | T-P7-E13-07 |
| F-QUOTA-FABLE-DETECT-VERIFY | Fable-quota detection verified complete | claude_usage.rs:312-402 real detection (explicit Fable window/model/limits signals; deliberate no-inference-from-aggregates), wired into parse output at :254. 15/15 unit tests pass incl. 3 Fable-specific | ✅ Done | T-P7-E12-04 |
| F-CLI-MODELS-UPDATER-VERIFY | `cascade update models` verified — real code, live refresh blocked by repo privacy | Fetch→validate→atomic-write path (update.rs:207-341) is complete and real; live run exits 1 with HTTP 404 because acamarata/cascade is PRIVATE and the raw.githubusercontent fetch is unauthenticated (file IS on origin/main). Works unmodified once repo is public; daemon falls back to compiled-in canonical meanwhile (model_drift.rs:76-96) | 🟡 Partial | T-P7-E12-04 |
| F-CLI-UPDATE-AUTO-PERIODIC | `cascade update auto` periodic behavior determination | DEFINITIVE: no periodic task exists at all — auto enable/disable only writes `[updates] auto` to config.toml (ipc_handlers.rs:599-616); flag has zero production readers (`get_auto_update` is #[allow(dead_code)]), no timer calls check_for_update/apply_update, scheduler store starts empty (supervisor.rs:365). Neither binary auto-apply nor models.yaml refresh runs on any schedule. Follow-up T-P7-E12-06 opened to wire the real 24h loop | 🟡 Partial | T-P7-E12-05 |
| F-APP-AUTO-UPDATE | Self-update | Tauri updater + GitHub Releases (signed); in-app update notification | ✅ Done | T-P4-E05-18 (release.yml signed updater artifacts) |

### 6b. Onboarding Wizard

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-WIZARD-FLOW | 10-phase wizard | Full first-run flow: detect harnesses, migrate configs, build cascade, connect providers, wire harnesses | 🔲 Planned | P3-E03 |
| F-WIZARD-RESUME | Checkpoint resume | Wizard state persisted at `~/.cascade/wizard-state.json`; resumable after crash or exit | 🔲 Planned | P3-E03 |
| F-WIZARD-SCANNER | Legacy tool home scanner | Discovers CC, OC, Codex, Cursor, Aider, Windsurf, Antigravity at global + per-project locations | 🔲 Planned | P3-E03 |
| F-WIZARD-AI-MERGE | AI-assisted merge engine | Parallel diff view; AI analyzes + merges legacy content into cascade format; per-section approve/reject/edit | 🔲 Planned | P3-E03 |
| F-WIZARD-ARCHIVE | Archive primitives | Move legacy configs to `.cascade/legacy/{tool}/` with manifest; non-destructive; `cascade restore` reverses | 🔲 Planned | P3-E03 |
| F-WIZARD-SYMLINKS | Symlink generation | Phase 8: create per-tool symlinks so tools read cascade content transparently | 🔲 Planned | P3-E03 |
| F-WIZARD-DAEMON-INSTALL | Daemon + widget install | Phase 9: install LaunchAgent/systemd/Windows Service; install OS widgets; start daemon | 🔲 Planned | P3-E03 |
| F-WIZARD-GEMINI-POOL | Build Gemini Pool | Wizard-guided: per-Gmail-account OAuth → GCP project → Gemini API key → add to Pool | 🔲 Planned | P3-E03 + P3-E04 |
| F-WIZARD-REVERSIBLE | Full reversibility | `cascade uninstall --full` restores all archived legacy homes and removes symlinks | 🔲 Planned | P3-E03 |
| F-DOC-WIZARD-PHASE-COUNT | Reconcile onboarding wizard phase-count mismatch across docs | Corrected `04-cascade-nomenclature-spec.md` § 5 from 8-phase to 10-phase table to match `WizardStep` enum (`Welcome=1..Done=10` in `types.ts`/`stepLabels.ts`); `05-cascade-product-architecture.md` § 5 declared canonical; doc 04 § 5 defers to it | ✅ Done | T-P7-E23-02 |

### 6c. OS Widgets

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-WIDGET-MACOS | macOS WidgetKit widget | Small/Medium/Large WidgetKit; tier rule counts, active project/phase, inbox, ideas, daemon age; 30s refresh | 🔲 Planned | P2-E04 |
| F-WIDGET-MACOS-MENUBAR | macOS menubar app | NSStatusItem: daemon status icon (green/amber/red), project count, click-to-open Cascade.app | 🔲 Planned | P2-E04 |
| F-WIDGET-LINUX-GNOME | Linux GNOME Shell extension | GJS extension for GNOME 45+: live status in top bar; 30s refresh | 🚧 In Progress | P2-E05 |
| F-WIDGET-LINUX-GNOME-CACHE-READER | GNOME cache reader | parseCache(filePath) in extension.js reads cache.json schema v1; fixture at fixtures/cache-v1.json | 🚧 In Progress | P2-E05 T-P2-E05-02 |
| F-WIDGET-LINUX-KDE | Linux KDE Plasmoid | QML Plasmoid for Plasma 5.27+/6.0+: live status in system tray; FullRepresentation popup with 6-row data grid + terminal launch | 🚧 In Progress | P2-E05 T-P2-E05-14 |
| F-WIDGET-WINDOWS | Windows 11 widget | Widget Board entry via WinUI 3 + Adaptive Cards; reads status cache | 🔲 Planned | P2-E06 |
| F-WIDGET-TRAY-CROSS-OS | Cross-OS system tray | `cascade-tray` Rust crate: unified NSStatusItem/AppIndicator3/Win32 Shell\_NotifyIcon abstraction | 🔲 Planned | P2-E06 |

---

## 7. RAG, MCP, Plugins, Distribution & Ops

### 7a. Local RAG & RRF Search Engine

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-RAG-FTS5 | FTS5 keyword index | SQLite FTS5 with porter tokenizer, stemming, prefix matching | 🔲 Planned | P4-E01 |
| F-RAG-BGE-M3 | E5 Large dense embeddings (`bge-m3` compatibility key) | fastembed-rs (ONNX); local-only; multilingual; native BGE-M3 remains deferred to E-P7-20 | ✅ Done | T-P7-E14-02 |
| F-RAG-FASTEMBED-FAIL-LOUD | Reject dense embedding without fastembed | Feature-disabled builds return an explicit capability error instead of zero vectors | ✅ Done | T-P7-E14-03 |
| F-RAG-NO-EPHEMERAL-MODELS | Refuse model downloads into ephemeral storage | Model weights are never fetched into an OS-temp-resident cache dir (the `$HOME`-override path that leaked multi-GB orphans); `CASCADE_ALLOW_TEMP_MODEL_DIR=1` overrides | ✅ Done | T-P7-E25-18 |
| F-RAG-RERANKER-FAIL-LOUD | Reject BGE reranking without reranker support | Feature-disabled builds return an explicit capability error instead of mock scores | ✅ Done | T-P7-E14-04 |
| F-RAG-MOCK-FALLBACK-STATUS | Surface lazy embedding degradation | Failed real-model init logs a warning and appears in daemon/CLI status | ✅ Done | T-P7-E14-05 |
| F-RAG-DEFERRED-TODO-AUDIT | Verify HyDE/feedback deferral tracking | HyDE is tracked by T-P7-E20-01; feedback projection/LoRA training is absent from the current backlog and E-P7-20 scope | ⚠️ Gap recorded | T-P7-E14-06 / T-P7-E20-01 |
| F-RAG-SPARSE | Sparse retrieval | TF-IDF ships today; native BGE-M3/SPLADE output remains deferred to E-P7-20 | 🟡 Partial | P4-E01 / E-P7-20 |
| F-RAG-SQLITE-VEC | sqlite-vec vector store | Dense vector store on top of SQLite; no external DB | 🔲 Planned | P4-E01 |
| F-RAG-RRF | RRF hybrid merger | Reciprocal Rank Fusion (k=60) across FTS5 + dense + sparse scores | 🔲 Planned | P4-E01 |
| F-RAG-RERANKER | bge-reranker-v2-m3 | Cross-encoder reranker via ONNX; opt-in; +~200MB | 🔲 Planned | P4-E01 |
| F-RAG-CHUNKERS | 4 chunker types | Semantic, hierarchical, markdown-aware, code-aware (tree-sitter) | 🔲 Planned | P4-E01 |
| F-RAG-PARSERS | 10 document parsers | markdown, Rust/TS/Python code, PDF, DOCX, XLSX, HTML, YAML, JSON, TOML | 🔲 Planned | P4-E01 |
| F-RAG-CITATIONS | Citation tracking | File path, line\_start, line\_end, chunk\_id, rrf\_score, source\_hash per result | 🔲 Planned | P4-E01 |
| F-RAG-AUTO-INDEX | Auto-RAG indexer | File watcher auto-indexes `.claude/memory/`, `.claude/planning/`, `.github/wiki/`, `docs/` | 🔲 Planned | P4-E01 |
| F-RAG-DND | Drag-and-drop ingest | Add any file to the index via Cascade.app drag-and-drop | 🔲 Planned | P4-E01 |
| F-RAG-EXTERNAL-DRIVE | External drive index | Point index root at any path (e.g. an external volume); daemon handles mount/unmount | 🔲 Planned | P4-E01 |
| F-RAG-MULTIVEC | Multi-vec MaxSim | Per-word E5 proxy is feature-gated; native BGE-M3/ColBERT remains deferred to E-P7-20 | 🟡 Partial | P4-E01 / E-P7-20 |
| F-RAG-EVAL | Offline eval harness | precision@k, recall@k, MRR, NDCG against golden query set | 🔲 Planned | P4-E01 |
| F-RAG-INCR-INDEX | Incremental indexing | Index only changed files; file-hash diffing | 🔲 Planned | P4-E04 |
| F-RAG-LRU-CACHE | LRU query cache | In-memory LRU cache for repeated queries | 🔲 Planned | P4-E04 |
| F-RAG-EMBED-CACHE | Persistent embedding cache | On-disk embedding cache to skip re-embedding unchanged chunks | 🔲 Planned | P4-E04 |
| F-RAG-LIVE-PUSH | Live-update IPC push | Daemon pushes index-freshness updates to connected clients as indexing progresses | 🔲 Planned | P4-E04 |
| F-RAG-SIGNED-DELTA | Signed delta updates | Index updates as signed deltas with rollback via `cascade rollback` | 🔲 Planned | P4-E04 |
| F-RAG-CONTEXT-COMPRESS | Context compression for harnesses | Compress/optimize the context window Cascade serves to CC/OC/Codex (rtk-equivalent) | 🔲 Planned | P4-E04 |
| F-RAG-HYDE | HyDE query expansion | Hypothetical Document Embeddings; stub at `retrieve/hyde.rs` | 🚫 Deferred | P5 |
| F-RAG-ONLINE-EVAL | Online A-B eval | Live A-B comparison of retrieval strategies | 🚫 Deferred | P5 |
| F-RAG-OCR | OCR for scanned PDFs | tesseract-rs for image-only PDFs | 🚫 Deferred | P5 |
| F-RAG-CROSS-FILE | Cross-file symbol resolution | "What uses this function" graph indexing | 🚫 Deferred | P5 |
| F-RAG-MULTI-PROJECT | Multi-project federated search | Single query spanning all project indexes | 🚫 Deferred | P5 |

### 7b. MCP Server

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-MCP-SERVER | MCP 2025-03 server | Rust MCP server in `cascade-mcp` crate; full spec compliance | 🔲 Planned | P4-E02 |
| F-MCP-RESOURCES | MCP resources primitive | `cascade.read(tier)`, codebase file resources | 🔲 Planned | P4-E02 |
| F-MCP-TOOLS | MCP tools primitive | `cascade.search()`, `cascade.search_codebase()`, inbox.send, memory.write, master\_lists | 🔲 Planned | P4-E02 |
| F-MCP-PROMPTS | MCP prompts primitive | Built-in named prompts via MCP | 🔲 Planned | P4-E02 |
| F-MCP-SAMPLING | MCP sampling primitive | Cascade as sampling client to LLM providers | 🔲 Planned | P4-E02 |
| F-MCP-LOGGING | MCP logging primitive | Structured log forwarding to MCP clients | 🔲 Planned | P4-E02 |
| F-MCP-TRANSPORTS | 5 MCP transports | Unix socket, stdio, SSE, HTTP/1.1, TCP with auth | 🔲 Planned | P4-E02 |
| F-MCP-CLIENT-CC | CC client config | Auto-configure `~/.claude/settings.json` mcpServers entry | 🔲 Planned | P4-E02 |
| F-MCP-CLIENT-DESKTOP | Claude Desktop config | Auto-configure Claude Desktop settings.json | 🔲 Planned | P4-E02 |
| F-MCP-CLIENT-OC | OC client config | Auto-configure `opencode.json` MCP entry | 🔲 Planned | P4-E02 |
| F-MCP-CLIENT-VSCODE | VS Code config | Auto-configure `.vscode/mcp.json` | 🔲 Planned | P4-E02 |
| F-MCP-TLS | TLS/mTLS for TCP port | LAN/remote MCP exposure over TLS | 🚫 Deferred | P5 |
| F-MCP-STREAMING | Streaming MCP responses | Server-sent event streaming from MCP server | 🚫 Deferred | P5 |
| F-MCP-USER-PROMPTS | User-defined prompts in MCP | User-authored prompt library via MCP | 🚫 Deferred | P5 |

### 7c. WASM Plugin System

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-PLUGIN-WASM | WASM plugin host | wasmtime execution engine; capability-based WASI permissions; hard resource limits | 🔲 Planned | P4-E03 |
| F-PLUGIN-ABI | Plugin WIT ABI | WIT-based interface; Chunker/Retriever/Provider/Agent/ToolIntegration traits | 🔲 Planned | P4-E03 |
| F-PLUGIN-MANIFEST | Plugin manifest schema | `plugin.json` schema + validation; `~/.cascade/plugins/<name>/` structure | 🔲 Planned | P4-E03 |
| F-PLUGIN-LIFECYCLE | Plugin lifecycle | Load, init, call, shutdown; hot-reload in dev mode | 🔲 Planned | P4-E03 |
| F-PLUGIN-PDK | Plugin development kit | `cascade plugin new` (cargo-generate), test harness, `cascade plugin test` | 🔲 Planned | P4-E03 |
| F-PLUGIN-DATA-SOURCES | First-party data source plugins | GitHub Issues, Linear, Jira, GitLab | 🔲 Planned | P4-E03 |
| F-PLUGIN-REGISTRY-CLI | Plugin registry CLI | `cascade plugin install/remove`; local `~/.cascade/plugins/` registry | 🔲 Planned | P4-E03 |
| F-PLUGIN-HOST-KV-LOG | Implement real KV/log in cascade-plugins WIT bindings | Host log imports read bounds-checked guest linear memory; plugin-scoped KV values persist in `<plugin-dir>/data/plugin-kv.sqlite3` across calls and reloads | ✅ Done | T-P7-E11-01 |
| F-PLUGIN-RESERVED-CAPS | Reclassify FsExec/NetListen as reserved capabilities | Manifests requesting either unimplemented capability fail explicitly with `CapabilityError::Reserved` | ✅ Done | T-P7-E11-02 |
| F-CORE-DEAD-WATCHER | Delete dead cascade_core watcher module | Removed superseded no-op watcher scaffold; daemon RAG and volume watchers remain the active implementations | ✅ Done | T-P7-E11-03 |
| F-CORE-DEAD-OAUTH | Delete dead fake OAuth module in cascade-core | Removed unwired placeholder crypto; production OAuth/PKCE remains in cascade-providers | ✅ Done | T-P7-E11-04 |
| F-PLUGIN-MARKETPLACE | Remote plugin marketplace | Hosted CDN discovery, ratings, `cascade plugin publish` | 🚫 Deferred | P5 |
| F-PLUGIN-SIGNING | Plugin signing/notarization | Code-signing for plugins in public registry | 🚫 Deferred | P5 |

### 7d. Security & Privacy

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-SEC-PROXY-HARDENING | Proxy/dashboard hardening | Local-auth HMAC token; path allowlist; header whitelist; 1MB body cap; 127.0.0.1 bind | 🔲 Planned | P2-E07 |
| F-SEC-PATH-TRAVERSAL | Path traversal guard | Reject `..` and null bytes in all resolve paths | 🔲 Planned | P2-E07 |
| F-SEC-PROMPT-INJECTION | Prompt injection detection | Flag embedded instruction sequences in content loader; log at WARN | 🔲 Planned | P2-E07 |
| F-SEC-CMD-INJECTION | Command injection protection | All shell invocations via `tokio::process::Command` arg arrays; no string interpolation | 🔲 Planned | P2-E07 |
| F-SEC-KEYCHAIN | OS keychain for API keys | macOS Security / Linux Secret Service / Windows Credential Manager via `cascade-keychain` | 🔲 Planned | P2-E07 |
| F-SEC-AUDIT-LOG | Tamper-evident audit log | Append-only JSONL, SHA-256 chain integrity, 0600 permissions | 🔲 Planned | P2-E07 |
| F-SEC-KEY-MIGRATION | vault.env → keychain migration | First-run migration offer from vault.env to OS keychain; vault.env fallback retained | 🔲 Planned | P2-E07 |
| F-SEC-CORS | Dashboard CORS policy | Allow-list origin (`http://127.0.0.1:9761` only); no wildcard | 🔲 Planned | P2-E07 |
| F-SEC-QUINN-PROTO | Upgrade quinn-proto past 0.11.14 (RUSTSEC-2026-0185) | Bumped transitive `quinn-proto` to 0.11.16 (remote memory exhaustion, high severity); `Cargo.lock`-only, no direct dependency | ✅ Done | T-P7-E02-03 |
| F-SEC-LOPDF | Upgrade lopdf past 0.34.0 (RUSTSEC-2026-0187) | Replaced with `pdf-extract` 0.12, gated behind optional `pdf-parser` feature in `cascade-rag` | ✅ Done | T-P7-E02-01 |
| F-SEC-QUICK-XML | Reconcile co-installed quick-xml versions | plist 1.9.0->1.10.0 unified onto the 0.41.0 line (clears RUSTSEC-2026-0194/0195 for plist+calamine); docx-rs's 0.36.2 has no upstream fix, added to cargo-audit ignore-list with documented rationale | ✅ Done | T-P7-E02-02 |
| F-SEC-WASMTIME-WASI | Real wasmtime/wasmtime-wasi upgrade (RUSTSEC-2026-0188) | Exact-pin bump 36.0.11->36.0.12 in workspace root + cascade-pdk/Cargo.toml | ✅ Done | T-P7-E02-04 |
| F-SEC-RING | Address ring 0.17.9 advisory (RUSTSEC-2025-0009) | Already resolved via prior Cargo.lock update to 0.17.14; verified no residual advisory | ✅ Done | T-P7-E02-05 |
| F-SEC-AUDIT-IGNORE-LIST | Fix cargo-audit CI ignore-list to not mask active vulns | Documented every ignored RUSTSEC ID with a reason comment in security.yml; added memmap2 (0.9.10->0.9.11) + spin (0.9.8->0.9.9) real fixes; added serial/ttf-parser (genuinely unmaintained, no fix) to the documented ignore-list | ✅ Done | T-P7-E02-06 |
| F-SEC-NODE-ADVISORIES | Fix Node dependency advisories (13 findings) | cascade-app already fixed (vite 6.4.3/vitest 3.2.6); cascade-dashboard was the actual remaining gap (vite 5.3.4/vitest 1.6.1, plus a mismatched coverage-v8@4 pinned against vitest@1) — bumped to match cascade-app, `pnpm audit` now clean workspace-wide | ✅ Done | T-P7-E02-07 |
| F-SEC-NPM-AUDIT-CI | Fix CI npm-audit ENOLOCK job | security.yml already used `pnpm audit` (not npm audit); the real bug was a hardcoded `pnpm/action-setup@v2 version:9` pin — removed in favor of `corepack enable` | ✅ Done | T-P7-E02-08 |

### 7e. Distribution & Ops

| ID | Feature | Description | Status | Delivers |
|---|---|---|---|---|
| F-DIST-HOMEBREW | Homebrew Cask | `brew install --cask cascade`; `acamarata/homebrew-cascade` tap | ✅ Done | T-P4-E05-09 |
| F-DIST-DMG | macOS DMG | Direct download from GitHub Releases | ✅ Done | T-P4-E05-18 |
| F-DIST-DEB-RPM | Linux .deb + .rpm | GitHub Releases artifacts | ✅ Done | T-P4-E05-18 |
| F-DIST-AUR | AUR package | `cascade-bin` PKGBUILD on aur.archlinux.org | ✅ Done | T-P4-E05-10 (USER-AUTH submission pending) |
| F-DIST-WINGET | Winget | `winget install acamarata.cascade`; PR to microsoft/winget-pkgs | ✅ Done | T-P4-E05-11 (USER-AUTH PR pending) |
| F-DIST-CARGO | cargo install | `cargo install cascade-cli`; crates.io publish | ✅ Done | T-P4-E05-12 |
| F-DIST-CHOCOLATEY | Chocolatey | nuspec + community feed submission | ✅ Done | T-P4-E05-13 (USER-AUTH submission pending) |
| F-DIST-SCOOP | Scoop | `acamarata/scoop-cascade` bucket manifest | ✅ Done | T-P4-E05-14 |
| F-DIST-SNAP | Snap | snapcraft.yaml + Snapcraft submission | ✅ Done | T-P4-E05-15 (USER-AUTH account pending) |
| F-DIST-FLATPAK | Flatpak | `io.github.acamarata.Cascade.yml` manifest | ✅ Done | T-P4-E05-16 (USER-AUTH Flathub PR pending) |
| F-DIST-NIX | Nix flake | `flake.nix` derivation | ✅ Done | T-P4-E05-17 |
| F-DIST-SIGNING-MACOS | macOS notarization | Apple Developer codesign + notarytool; USER-AUTH cert gate | ✅ Done | T-P4-E05-06 (USER-AUTH Apple enrollment pending) |
| F-DIST-SIGNING-WINDOWS | Windows Authenticode | SignPath.io FOSS cert; USER-AUTH project creation gate | ✅ Done | T-P4-E05-07 (USER-AUTH SignPath enrollment pending) |
| F-DIST-SIGNING-LINUX | Linux GPG signing | GPG release key per-distro; fingerprint in README | ✅ Done | T-P4-E05-05 + T-P4-E05-08 |
| F-DIST-CI-RELEASE | Release CI/CD | GitHub Actions matrix: macOS ARM/x64, Linux x64/ARM64, Windows x64; sign + package + publish on tag | ✅ Done | T-P4-E05-18 |
| F-DOC-LAUNCH-WIKI | Confirm/complete the 4 launch wiki pages | Audited Installation, CLI Reference, Cascade Concepts, and Quickstart against current CLI and installer source; corrected drift and completed the launch guidance | ✅ Done | T-P7-E23-01 |
| F-OBS-TRACING | OpenTelemetry tracing | OTel spans across daemon + CLI; structured JSON logs; 7-day rotation | 🔲 Planned | P2-E01 |
| F-OBS-HEALTHCHECK | Health endpoint | `/health` returns `{status:'ok'}` only; no internal stats leaked | 🔲 Planned | P2-E07 |
| F-CI-ESLINT-PURITY | Fix 5 impure-render / ref-write eslint errors | useAccounts/useChat/usePewsTree/useIngestProgress — moved Date.now() calls and ref writes out of render into effects/callback-time state | ✅ Done | T-P7-E01-03 |
| F-CI-MARKDOWNPREVIEW-FLAKE | Confirm or fix markdownPreview.test.tsx jsdom flake | Reproduced (~20-30% of isolated runs, unhandled rejection from a fire-and-forget userEvent.click after teardown); fixed via synchronous fireEvent.click | ✅ Done | T-P7-E01-06 |
| F-CI-LEAN-BINARY-ASSERT-ORDER | Fix daemon-ci.yml lean-binary assert running against the wrong build | "Assert lean binary excludes network surfaces" ran after the all-features build had already overwritten target/debug/cascaded; reordered to run right after the lean build | ✅ Done | E-P7-01/W-01 |
| F-CI-APP-HOSTED-BILLING | Fix ci-app.yml spending unconditional paid GitHub-hosted minutes | Private repo was running macOS/Ubuntu/Windows Tauri builds unconditionally on GitHub-hosted runners; split into self-hosted build-linux (always-on) + gated build-hosted (vars.HOSTED_CI, macOS/Windows only) | ✅ Done | E-P7-01/W-01 |
| F-CI-MCP-HARNESS-FIXTURE | Fix cascade-mcp c_harness_setup_prompt ambient-state dependency | Test depended on `~/.claude/CLAUDE.md` existing on the runner; always passed on dev machines, failed on a clean CI HOME. Uses the same fixture HOME as a_resource_surface now | ✅ Done | E-P7-01/W-01 |

---

## 8. Deferred / Future

Items explicitly deferred beyond P4 or assigned to the ClawDE fork. Do not build in P2–P4.

| ID | Feature | Description | Status | Target |
|---|---|---|---|---|
| F-CLAWDE-NATIVE-DB | ClawDE native DB + nSelf-sync | Full native DB + remote sync rewrite; separate product | 🚫 Deferred | ClawDE fork |
| F-P5-HYDE | HyDE query expansion | See P5 list — stub exists in `retrieve/hyde.rs` | 🚫 Deferred | P5 |
| F-P5-ONLINE-EVAL | Online A-B RAG eval | Live retrieval strategy comparison | 🚫 Deferred | P5 |
| F-P5-OCR | OCR for scanned PDFs | tesseract-rs | 🚫 Deferred | P5 |
| F-P5-CROSS-FILE | Cross-file symbol graph | "What uses this function" indexing | 🚫 Deferred | P5 |
| F-P5-MULTI-PROJECT | Multi-project federated search | Single query across all indexes | 🚫 Deferred | P5 |
| F-P5-MCP-TLS | TLS/mTLS for MCP TCP | LAN/remote MCP exposure | 🚫 Deferred | P5 |
| F-P5-MCP-STREAMING | Streaming MCP responses | SSE streaming from server | 🚫 Deferred | P5 |
| F-P5-MCP-USER-PROMPTS | User-defined MCP prompts | User-authored prompt library via MCP | 🚫 Deferred | P5 |
| F-P5-PLUGIN-MARKETPLACE | Plugin marketplace | Hosted CDN + ratings + `cascade plugin publish` | 🚫 Deferred | P5 |
| F-P5-PLUGIN-SIGNING | Plugin signing/notarization | Public registry code-signing | 🚫 Deferred | P5 |
| F-P5-MAS | Mac App Store distribution | Sandbox restrictions complicate daemon; direct first | 🚫 Deferred | P5+ |
| F-P5-MSSTORE | Microsoft Store distribution | Same constraint as MAS | 🚫 Deferred | P5+ |
| F-P5-CURSOR-AIDER | Cursor/Aider/Windsurf MCP configs | P4 covers CC/Desktop/OC/VS Code only | 🚫 Deferred | P5 |
| F-P5-MULTI-USER | Multi-user/team access controls | Single-user only for P2–P4 | 🚫 Deferred | P5+ |
| F-P5-TEMPLATE-MARKETPLACE | Template marketplace | Hosted template discovery server | 🚫 Deferred | P5 |
| F-P5-GPU-ACCEL | GPU/Metal acceleration for RAG | CPU ONNX only in P4; CUDA/Metal path is P5 | 🚫 Deferred | P5 |
| F-P5-MULTIVEC-COMPRESS | Multi-vec compression | Scalar/product quantization for token matrices | 🚫 Deferred | P5 |
| F-P5-I18N | Internationalization of docs + UI | Translated docs and localized UI | 🚫 Deferred | P5 |

---

## Feature Count Summary

| Section | ✅ Done | 🟡 Partial | 🔲 Planned | ➕ New | 🚫 Deferred | Total |
|---|---|---|---|---|---|---|
| 1. Identity & Principles | 5 | 0 | 2 | 0 | 1 | 8 |
| 2. Instruction Cascade | 0 | 0 | 9 | 0 | 1 | 10 |
| 3. Knowledge & Memory | 0 | 0 | 14 | 0 | 0 | 14 |
| 4. Fleet, Quota & Gemini Pool | 1 | 0 | 9 | 0 | 0 | 10 |
| 5. Daemon, CLI & Harness Bridge | 0 | 0 | 37 | 0 | 0 | 37 |
| 6. Tauri App + Wizard + Widgets | 2 | 1 | 34 | 0 | 0 | 37 |
| 7. RAG, MCP, Plugins, Dist & Ops | 0 | 0 | 62 | 0 | 18 | 80 |
| 8. Deferred / Future | 0 | 0 | 0 | 0 | 19 | 19 |
| **Total** | **6** | **0** | **169** | **0** | **39** | **214** |

*Note: ✅ Done features are architectural constants established by design decisions, not shipped code. Cascade P2 build has not started; no features are code-complete yet.*
