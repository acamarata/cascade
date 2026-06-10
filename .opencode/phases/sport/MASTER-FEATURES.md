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
| F-IDENTITY-FOSS | FOSS, MIT license | All Cascade code MIT; no telemetry; user owns data | ✅ Done | Architecture |
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
| F-RAG-BGE-M3 | BGE-M3 dense embeddings | fastembed-rs (ONNX); local-only; multilingual; ~500MB model | 🔲 Planned | P4-E01 |
| F-RAG-SPARSE | Sparse embeddings (SPLADE/BM25) | BGE-M3 sparse output stored alongside dense vectors | 🔲 Planned | P4-E01 |
| F-RAG-SQLITE-VEC | sqlite-vec vector store | Dense vector store on top of SQLite; no external DB | 🔲 Planned | P4-E01 |
| F-RAG-RRF | RRF hybrid merger | Reciprocal Rank Fusion (k=60) across FTS5 + dense + sparse scores | 🔲 Planned | P4-E01 |
| F-RAG-RERANKER | bge-reranker-v2-m3 | Cross-encoder reranker via ONNX; opt-in; +~200MB | 🔲 Planned | P4-E01 |
| F-RAG-CHUNKERS | 4 chunker types | Semantic, hierarchical, markdown-aware, code-aware (tree-sitter) | 🔲 Planned | P4-E01 |
| F-RAG-PARSERS | 10 document parsers | markdown, Rust/TS/Python code, PDF, DOCX, XLSX, HTML, YAML, JSON, TOML | 🔲 Planned | P4-E01 |
| F-RAG-CITATIONS | Citation tracking | File path, line\_start, line\_end, chunk\_id, rrf\_score, source\_hash per result | 🔲 Planned | P4-E01 |
| F-RAG-AUTO-INDEX | Auto-RAG indexer | File watcher auto-indexes `.claude/memory/`, `.claude/planning/`, `.github/wiki/`, `docs/` | 🔲 Planned | P4-E01 |
| F-RAG-DND | Drag-and-drop ingest | Add any file to the index via Cascade.app drag-and-drop | 🔲 Planned | P4-E01 |
| F-RAG-EXTERNAL-DRIVE | External drive index | Point index root at any path (e.g. `/Volumes/X9/`); daemon handles mount/unmount | 🔲 Planned | P4-E01 |
| F-RAG-MULTIVEC | Multi-vec ColBERT | BGE-M3 multi-vector mode for best recall+precision (highest disk cost) | 🔲 Planned | P4-E01 |
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
| F-OBS-TRACING | OpenTelemetry tracing | OTel spans across daemon + CLI; structured JSON logs; 7-day rotation | 🔲 Planned | P2-E01 |
| F-OBS-HEALTHCHECK | Health endpoint | `/health` returns `{status:'ok'}` only; no internal stats leaked | 🔲 Planned | P2-E07 |

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
| 4. Fleet, Quota & Gemini Pool | 0 | 0 | 9 | 0 | 0 | 9 |
| 5. Daemon, CLI & Harness Bridge | 0 | 0 | 37 | 0 | 0 | 37 |
| 6. Tauri App + Wizard + Widgets | 0 | 0 | 36 | 0 | 0 | 36 |
| 7. RAG, MCP, Plugins, Dist & Ops | 0 | 0 | 62 | 0 | 18 | 80 |
| 8. Deferred / Future | 0 | 0 | 0 | 0 | 19 | 19 |
| **Total** | **5** | **0** | **169** | **0** | **39** | **213** |

*Note: ✅ Done features are architectural constants established by design decisions, not shipped code. Cascade P2 build has not started; no features are code-complete yet.*
