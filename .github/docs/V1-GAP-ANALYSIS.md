# Cascade v1 — Gap Analysis & Remaining-Work Plan

_Generated 2026-06-11 from a 17-agent code sweep of the shipped P2-P4 implementation against the full product vision._

## Executive summary

Cascade has shipped 450 tickets across P2-P4 and the core machinery is real: six-tier resolver, RAG/RRF, MCP server (8 read/write tools), WASM plugin runtime, providers registry, Tauri app + browser dashboard, GFP provisioning client, local-LLM crate, harness codex/cursor scaffolding, scheduled-task store, vault, library. But the headline vision claims — "orchestrates agent teams incl a T0 CEO agent," "ONE merged cascade to each harness," "bootstraps AI access," and "Jira replacement" — are NOT yet realized in code. Verified state: the AgentPlugin trait is deliberately single-shot (run(), explicitly no multi-step loop) and is never spawned from the daemon or CLI; dispatch is single-shot to cc|oc only; there is no orchestrator, agent registry, task queue, or agent-to-agent inbox; LocalLlmAdapter is built but never registered in the daemon; GFP full_auto is hardcoded n=1 with no multi-key/multi-account loop; cascade init is GUI-wizard-only with no headless/--accept-defaults/--provider flags; there is no cascade verify, no curl|bash installer, and daemon install is macOS-only; there is no unified provide_harness_context MCP tool (each harness still reads per-tier files); there is no Task/Kanban data model (task_store.rs is the cron scheduler, InboxPage is a stub); and there are no native PBD phase/ticket CLI commands or MCP tools. The remaining work splits cleanly into five phases (P5-P9): P5 setup-reliability + provider wiring (the foundation everything else needs to actually run), P6 agent orchestration (the T0 CEO + dispatch loop, the single biggest vision gap), P7 unified-cascade delivery + GFP scale-out, P8 PBD/Kanban (the Jira-replacement ask), and P9 knowledge/editor polish + optional CC-proxy beta. P5, P6, and the unified-cascade half of P7 are the v1 release blockers; analyzer/EIE and the CC-interactive-proxy are explicitly post-v1.

## Coverage by pillar

| Pillar | Coverage |
|---|---|
| fts (first-time setup) | mostly — Tauri wizard + macOS install.sh ship, but no headless CLI init, no curl|bash installer, no cross-platform daemon install, no cascade verify. P5 closes this. |
| gfp (Gemini Free Pool) | partial — provisioning client + OAuth wiring exist but full_auto is hardcoded n=1; no multi-key/multi-account loop, no pool-exhaustion monitor, no quota strategy. P7 closes this. |
| local-llm | mostly — crate + downloader + Metal exist but LocalLlmAdapter is never registered in the daemon and there is no models-download CLI or wizard step. P5 wires it; P6 routes agent work to it. |
| subs (subscriptions) | partial — Cursor/Codex detected, OpenCode adapter ships, but no Cursor/Antigravity provider adapters and no multi-subscription arbitration. P7 adds routable adapters. |
| tiers | mostly — six-tier resolver works but PCI/APC classification is buggy (~/Sites mis-tagged), CASCADE_APC_PATH not bound in discovery, cascade_resolve IPC is a stub. P5 fixes classification + resolve IPC. |
| symlink / folder choice | mostly — symlink + generate-instructions ship but hardcoded to .cascade; no --folder choice (.claude/.codex), no fallback chain, no migrate-folder. P5 adds folder preference. |
| personal (PCI vault) | partial — vault UI exists but hardcoded to ~/.cascade; PCI at ~/Downloads/.cascade not surfaced, not in memory/inbox aggregation. P9 wires PCI throughout the UI. |
| unified (one merged cascade per harness) | mostly — resolver + policy engine exist but no provide_harness_context MCP tool; harnesses still read per-tier files and reconcile locally; only cc/oc get generate-instructions. P7 ships the unified payload + per-harness onboarding. |
| rag | mostly — RRF + dense/sparse + MCP retrieval ship; HyDE/BGE-M3-sparse/ColBERT are stubbed or TF-IDF-fallback; no agent-runtime integration. P6 adds agent-context retrieval; P9 ships HyDE/M3/ColBERT upgrades. |
| editor (instructions+memory browser) | mostly — MemoryViewer/CascadeTierTree/GraphView ship but instructions are not a queryable object, no /vault/instructions route, no instruction full-text search or tier diff. P9 closes this. |
| kanban | absent — no Task data model, no board, no task CRUD IPC; task_store.rs is the cron scheduler and InboxPage is a stub. P8 builds the whole thing. |
| pews (native PBD) | partial — phase YAML is read but there are no native phase/ticket/eot/eop CLI commands, no PBD MCP tools, no mutation UI. P8 builds the native PBD engine (the founder's headline ask). |
| eie (engineering-excellence analysis) | absent — no cascade-analyzer, no file-size/DRY/dup/lint analysis, no health dashboard, no code-metrics MCP resources. Deferred to P9 stretch / post-v1; not a release blocker. |
| agents (T0 CEO orchestration) | minimal — Tier enum + AgentPlugin trait exist but the trait is deliberately single-shot, never spawned; no orchestrator, registry, task queue, agent inbox, or founder-facing CEO endpoint. P6 is dedicated to this — the single biggest vision gap. |
| promptbox | mostly — browser GP Chat panel ships but Tauri app DashboardPage is a placeholder; chat is hardcoded to the Gemini proxy with no provider-routing/local-LLM fallback and no in-app MCP tools. P7 (routing) + P9 (in-app panel) close it. |
| ccproxy (CC-as-API) | absent — no HTTP/SSE bridge wrapping claude -p, no CC auth/session/pooling. Explicitly an optional beta; P9 ships it feature-flagged OR it is cut from v1 per founder decision. |

## Plan: 5 phases · ~320 tickets

### [P5] Setup Reliability & Provider Wiring  (~60 tickets)

**Goal:** Make Cascade installable and fully operational with zero GUI dependency: headless init, cross-platform daemon, a real verify/health gate, correct tier resolution, folder choice, and the local-LLM adapter actually registered. This is the foundation — nothing above works if a fresh machine can't reach a wired, resolvable, provider-backed daemon.

Pillars: fts, tiers, symlink, local-llm, subs

- E-P5-01 Headless init: `cascade init --accept-defaults --provider {gemini|anthropic|openai|local} --api-key $KEY --folder {.cascade|.claude|.codex}`; auto-tier detection; blank CASCADE.md scaffolding; no Tauri dependency
- E-P5-02 Cross-platform daemon install: unify macOS launchd + Linux systemd unit + Windows Service in a cascade_install path; build/package/ship the cascaded binary; verify binary presence before writing service files
- E-P5-03 curl|bash installer (install.cascade.dev): OS detect, download correct prebuilt binary, run cascade init --accept-defaults, run cascade verify
- E-P5-04 `cascade verify` post-init healthcheck: all six tier files readable, daemon socket reachable, context resolves for CWD, registered providers respond; exit 0 on green
- E-P5-05 Tier classification fix: map ~/Sites→APC and ~/Downloads→PCI before heuristic fallback; bind CASCADE_APC_PATH into TierDiscovery; implement the cascade_resolve daemon IPC handler (currently a stub)
- E-P5-06 Folder-preference mechanism: --folder flag + config.toml ai_folder_choice + fallback walk-up chain (.claude→.codex→.opencode→.cascade) + `cascade migrate-folder`
- E-P5-07 Local-LLM wiring: add cascade-local-llm to the daemon, register LocalLlmAdapter as local:<model> on startup when ~/.cascade/models/<id> exists; `cascade models download <id>` with real SHA-256 checksums; wizard offline-fallback step
- E-P5-08 Provider credential CLI + OAuth stub completion: finish provider_connect; wire --provider/--api-key through init; persist to keychain

### [P6] Agent Orchestration & the T0 CEO  (~80 tickets)

**Goal:** Realize the headline vision: a T0 CEO agent that talks to the human founder and spawns/coordinates a fleet of tiered agents (coding and non-coding). Turn the single-shot AgentPlugin trait and single-shot dispatch into a real multi-agent runtime with a registry, task queue, multi-step loop, agent-to-agent inbox, and a founder-facing control endpoint. This is the largest and most differentiating phase.

Pillars: agents, rag, local-llm

- E-P6-01 Agent registry + capability matching: cascade-types agent registry, skill/capability tags, ID/versioning scheme, role matrix for task routing
- E-P6-02 Multi-agent dispatch loop: daemon agent task queue, parallel execution coordination, ReAct-style multi-step turn tracking, model/tier routing (incl. local-LLM for cheap tiers)
- E-P6-03 Wire AgentPlugin lifecycle: call path from daemon to run the WASM agent; populate AgentContext (messages, available_tools from MCP registry, session, memory injection)
- E-P6-04 T0 CEO / Founder Orchestrator: designate a T0 agent that receives top-level human directives via REST/IPC, decomposes them, spawns child agents, reports back; conversation/state persistence
- E-P6-05 Agent-to-agent inbox: inter-agent message queue + handoff protocol (mirrors cascade.inbox.* but agent-scoped)
- E-P6-06 Tool registry & capability grants: centralized tool discovery, per-agent grants (read/search/call-LLM/write), bind MCP tools + library tool refs into one ToolIntegration contract
- E-P6-07 Non-coding automation: hook-triggered autonomous agents for email drafting / ticket triage / workflow runs (data-source agent → LLM → template)
- E-P6-08 Chains/workflows DSL: composable ChainStep/ChainFlow async executor + YAML spec for multi-step agent workflows
- E-P6-09 Agent library schema + validation: formalize on-disk agent/persona/prompt structure, JSON-schema validator, cascade doctor check, pre-commit hook

### [P7] Unified Cascade Delivery & AI-Access Scale-Out  (~65 tickets)

**Goal:** Deliver ONE merged instruction+policy+config payload to every harness in a single call, extend that to all five harnesses (CC/OC/Codex/Cursor/Aider), and make AI access abundant: GFP multi-key/multi-account provisioning to the real MAX, routable subscription adapters, and provider-routing with fallback in the prompt box.

Pillars: unified, gfp, subs, promptbox

- E-P7-01 provide_harness_context MCP tool: returns {merged_instructions, policies, config, mcp} for a named harness; harness identity in MCP request context; eliminates per-harness file reconciliation
- E-P7-02 cascade.toml [policy] + [harness] schema: per-tier policy scoping (global|project|repo) with inheritance; per-harness config block; pre-evaluate + cache policies per project at daemon startup
- E-P7-03 generate-instructions for all harnesses: --output-single-file unified merged file; extend beyond cc/oc to Codex/Cursor/Aider; `cascade harness init-from-installed` autodetect + scaffold + wire
- E-P7-04 GFP multi-key provisioning loop: full_auto(email, n) loop with count param from UI; ProvisionBatch handler + progress; resumable provisioning-state.json checkpoint
- E-P7-05 GFP multi-account + quota strategy: multi-Google-account import, round-robin/sequential key creation, GCP 25-project-per-account modeling, pool-exhaustion monitor daemon task, quota-estimation UI
- E-P7-06 Routable subscription adapters: CursorAdapter + AntigravityAdapter implementing ProviderAdapter (route Chat/CodeCompletion to their engines); dynamic OpenCode model discovery; multi-subscription arbitration policy
- E-P7-07 Prompt-box provider routing: chat handler consults ProviderRegistry.default_for_task instead of hardcoded Gemini proxy; local-LLM streaming fallback; provider-selection UI

### [P8] Native PBD & Kanban — the Jira Replacement  (~55 tickets)

**Goal:** Make Cascade the task/phase system of record for AI-assisted dev. Build the Task/Kanban data model + boards and native PBD phase/ticket lifecycle (CLI + MCP + UI), so agents and humans share one mutable source of truth and tickets feed directly into harness context.

Pillars: kanban, pews

- E-P8-01 Task data model + store: cascade-types Task (id/title/status/project/tags/assignee/priority/blockers/dates) + SQLite store + task_create/update/list/delete IPC
- E-P8-02 Kanban boards UI: per-project status columns, drag-drop CRUD, replace InboxPage stub; cross-project aggregation in the Personal dashboard panel; task search/filter via RRF
- E-P8-03 Native PBD CLI: cascade phase list/open/close/archive/new, cascade ticket create/list/update/close, cascade sprint status, cascade eot/eos/eop equivalents in cascade-core::pbd
- E-P8-04 PBD MCP tools: read_phase_status, list_tickets, update_ticket_status, batch_close_tickets, create_phase, archive_phase (paired with ticket-YAML read resources)
- E-P8-05 PEWS mutation UI + sync: editable ReactFlow ticket nodes, on-canvas depends_on linking, status dropdowns; status.yaml ↔ app sidebar live sync via file-watcher
- E-P8-06 PEWS validation/repair + SPORT masters: linter (parseable, depends_on resolves, no cycles, valid status) with --repair; MASTER-PHASES/TICKETS/PEWS docs
- E-P8-07 Task↔context injection: pack active-sprint tickets into harness context (CC/OC); link tasks to phase/epic/sprint for AI planning

### [P9] Knowledge Polish, Personal Vault & Optional Beta  (~60 tickets)

**Goal:** Close the remaining knowledge/UX gaps that make the merged-cascade vision feel whole: surface the personal (PCI) vault everywhere, make instructions a first-class queryable object, integrate the in-app prompt box, ship the deferred RAG upgrades, and decide/ship the optional CC-as-API beta behind a flag.

Pillars: personal, editor, promptbox, rag, eie, ccproxy

- E-P9-01 Personal (PCI) vault end-to-end: surface ~/Downloads/.cascade in AppState + VaultContext + /vault/personal route; include PCI in memory/inbox/threads aggregation; onboarding + settings PCI path override; personal daily notes
- E-P9-02 Instructions as knowledge object: /vault/instructions browser (all six tiers), instruction full-text search across CASCADE.md, tier diff/inheritance view, optional instruction nodes in GraphView, instruction↔memory cross-linking
- E-P9-03 In-app prompt box: port GP Chat into the Tauri app (DashboardPage placeholder → real panel), wire Tauri invoke → daemon /api/chat, daemon-backed history, in-app MCP tool invocation, added_subs/cascaded context injection
- E-P9-04 RAG upgrades: wire HyDE into live queries (per SearchConfig), upgrade fastembed for true BGE-M3 SPLADE sparse (drop TF-IDF fallback), integrate ColBERT multivec as optional 4th RRF channel — all behind feature/config flags with golden-set regression
- E-P9-05 (stretch) EIE code-analysis layer: cascade-analyzer (file metrics, duplication, dep order), cascade health subcommand, code-metrics MCP resources, lint-policy bridge — post-v1 candidate
- E-P9-06 (optional beta) CC-as-API proxy: cascade-ccapi crate wrapping claude -p as HTTP POST + SSE stream, CC auth/session handling, token-bucket quota, settings.json [experimental] flag (default off); ships only if founder greenlights for v1

## Release blockers (must exist before calling it v1)

- Headless, GUI-free setup must work end-to-end: a fresh machine reaches a wired, resolvable daemon via `cascade init --accept-defaults` + `cascade verify` exiting 0, on macOS AND Linux AND Windows (E-P5-01..04). Today init is Tauri-wizard-only and verify does not exist.
- At least one AI-access path must be self-bootstrapping without manual key entry: either GFP provisions ≥1 working key automatically OR the local-LLM adapter is registered and serves inference. Today LocalLlmAdapter is never registered and provider_connect is a stub (E-P5-07, E-P5-08).
- The T0 CEO agent + multi-agent dispatch loop must exist and actually spawn/coordinate ≥1 child agent end-to-end. This is the product's headline claim ('orchestrates agent teams incl a T0 CEO agent'); without it Cascade is a context resolver, not what the vision describes (E-P6-02, E-P6-03, E-P6-04).
- ONE merged cascade must reach every supported harness via a single mechanism: provide_harness_context MCP tool + generate-instructions covering CC/OC/Codex/Cursor/Aider. Today harnesses read per-tier files and reconcile locally, and only cc/oc are wired (E-P7-01, E-P7-03).
- Tier resolution must be correct: ~/Sites→APC / ~/Downloads→PCI classification fixed and the cascade_resolve daemon IPC implemented (not a stub). A misclassified cascade silently feeds harnesses the wrong merged truth (E-P5-05).
- Native PBD phase/ticket lifecycle + Kanban board must ship if v1 is to satisfy the inbound 'Jira replacement' feature request — at minimum task CRUD + per-project board + phase/ticket CLI (E-P8-01..04). If the founder de-scopes this from v1, it is not a blocker; flagged as open question.

## Top risks

- Agent orchestration (P6) is the largest, least-de-risked piece and is the literal headline of the product. The trait is deliberately single-shot today; building a safe multi-step loop with budget/loop guards, model routing, and a founder-facing CEO is genuinely hard and easy to under-estimate. Slipping P6 slips the whole v1 identity.
- GFP aggressive multi-key/multi-account provisioning (P7) risks Google ToS / abuse-detection action against the founder's real Google accounts. Hardcoded n=1 is conservative; scaling to 28+ keys across many accounts could get accounts flagged or banned. Needs an explicit, founder-approved aggressiveness ceiling and cool-down strategy before shipping the loop.
- Routing work to other vendors' subscription engines (Cursor, Antigravity, CC-as-API) may violate each vendor's ToS (reselling/proxying paid inference) and depends on undocumented IPC/session internals that break on every vendor update. High maintenance burden + legal exposure for arguably low v1 value.
- Cross-platform daemon install (Linux systemd + Windows Service) is currently macOS-only; Windows Service implementation in Rust is fiddly and CI for all three platforms (incl. Metal GPU path) is unvalidated. Setup reliability is the foundation everything stands on, so flaky installs poison first impressions.
- Scope sprawl: five phases at ~320 tickets is comparable to all prior work combined. Treating EIE/analyzer and CC-proxy as must-haves rather than post-v1 would inflate this further. Discipline on what 'discussion-ready v1' means is essential.
- The deferred RAG upgrades (BGE-M3 sparse, ColBERT) depend on upstream fastembed adding EmbeddingModel::BGEM3, which is outside the team's control — could block E-P9-04 indefinitely; keep TF-IDF fallback as the shipping default.

## Open questions for the founder (and the SAFE defaults I am building under)

1. **Q:** Default AI-folder convention: should a fresh `cascade init` default to .cascade (neutral, current behavior), or auto-adopt an existing .claude/.codex if present? This sets the out-of-box identity for every new user and is hard to change later.
   - DEFAULT: keep `.cascade` neutral, but auto-adopt an existing `.claude`/`.codex` if present (non-destructive). `--folder` choice built regardless.

2. **Q:** GFP key-creation aggressiveness ceiling: what is the MAX we should auto-provision per Google account and across how many accounts? Is the target ~28 keys total, or 28 per account across N accounts (150+)? This trades pool size against real ToS/ban risk on YOUR Google accounts — set the ceiling and the cool-down policy.
   - DEFAULT (SAFE): conservative ceiling — a few keys per account behind cool-downs, auto-max OFF by default; opt-in to higher. Protects your real Google accounts from ToS/ban.

3. **Q:** Is native PBD + Kanban (the inbound 'Jira replacement' request) in-scope for v1, or a fast-follow v1.1? It's a full phase (~55 tickets) and the answer determines whether P8 is a release blocker or deferred.
   - DEFAULT: YES, P8 ships in v1 (your inbox PCI is High priority + headline differentiator).

4. **Q:** Does the CC-interactive-proxy (CC-as-API) ship in v1 as an off-by-default experimental beta, or is it cut entirely until post-v1? It carries ToS/maintenance risk and is the weakest-value blocker candidate.
   - DEFAULT: build CC-as-API proxy as an OFF-by-default experimental flag; not enabled.

5. **Q:** Subscription routing to other vendors (Cursor, Antigravity): do we route actual inference work to their engines (ToS-risky, brittle), or limit Cascade to detecting/hinting identity and generating their config files (safe, current behavior)? This decides whether the subs pillar is 'routable' or 'detect-only' for v1.
   - DEFAULT (SAFE): detect + generate-config only; do NOT route real inference to other vendors' engines (avoids ToS/legal exposure). Inference-routing left as a flagged stub.

6. **Q:** For the T0 CEO agent: how much autonomy at launch — does it execute non-coding actions (send email, file tickets, run workflows) directly, or only draft-and-await-founder-approval? This sets the default trust boundary for the most powerful piece of the product.
   - DEFAULT (SAFE): T0 CEO drafts-and-awaits-approval for non-coding/outbound actions (email, tickets); auto-executes only sandboxed read/search/internal-coding.

7. **Q:** EIE / code-analysis layer (cascade-analyzer, health dashboard): confirmed post-v1 stretch, or do you want even a minimal file-size/duplication health check in v1?
   - DEFAULT: minimal file-size/duplication health check in v1; full analyzer post-v1.
