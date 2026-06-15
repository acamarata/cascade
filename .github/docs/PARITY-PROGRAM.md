# Cascade Parity Program

**Goal:** make Cascade fully replace the maintainer's hand-built local AI-coding setup — reach parity on every capability, then exceed it. Each phase ships as a production-quality `0.9.x` patch release via its EOP gate. `v1` is reserved and out of scope until explicitly called.

**Status:** active. Source gap analysis: six-domain deep audit (2026-06-14). Predecessor: [`V1-GAP-ANALYSIS.md`](V1-GAP-ANALYSIS.md).

---

## Version mapping

| Phase | Theme | Release | Gate |
|---|---|---|---|
| P11 | Make Cascade *correct* (foundational fixes) | `0.9.1` | EOP |
| P12 | Content parity (rules + corpus) | `0.9.2` | EOP |
| P13 | Runtime integration (settings + hooks) | `0.9.3` | EOP |
| P14 | Execution engine (`/plan` + `/build` + audit triad) | `0.9.4` | EOP |
| P15 | Ops + memory refinement | `0.9.5` | EOP |
| P16+ | Beyond parity (Cascade-only wins) | `0.9.6+` | EOP |

**Release rule:** a phase only ships when its build is 100% green (cargo build + clippy `-D warnings` + tests + `cargo deny` + relevant integration checks). No partial releases. Patch bump is auto-authorized at True-100%-Done per phase; minor/major and `v1` require explicit instruction.

---

## Scorecard (origin state, pre-P11)

| Domain | Absent | Partial | Cascade-better | Local more refined |
|---|---|---|---|---|
| PBD execution engine | 13 | 8 | 8 | — |
| Multi-agent orchestration | 6 | 5 | — | — |
| Memory / recall / knowledge | 5 | 3 | 2 | 5 |
| Runtime guardrails / hooks | 12 | 3 | — | — |
| Ops / fleet / infra | 3 | 5 | — | — |
| Cascade mechanics / corpus | 7 | 4 | — | 11 |
| **Total** | **46** | **28** | **10** | **16** |

### Four critical "incomplete despite appearing present" findings (all P11)
1. `cascade.search` returns a placeholder string — the RAG pipeline is built but never wired into MCP (`crates/cascade-mcp/src/tool.rs`).
2. `ChainStep::Parallel` runs sequentially (`crates/cascade-agents/src/chain.rs` — "true parallelism out of scope"); `CeoOrchestrator` never honors `SubGoal.parallel`.
3. Instructions are flattened into one `merged_instructions` blob (`crates/cascade-core/src/cascade_resolve.rs`) — no always-loaded vs on-demand split. Context-budget **regression** vs local.
4. No tier→model-ID registry — `model_pref` is `None` everywhere.

---

## P11 — Make Cascade correct (`0.9.1`)

Foundational fixes everything else depends on. Crate-grouped for parallel isolation.

**E11.1 — Search & retrieval (crates: cascade-mcp, cascade-rag)**
- T11.1.1 Inject `Arc<dyn Retriever>` into `ToolRegistry` at construction.
- T11.1.2 Replace `cascade.search` stub with a live RRF call into the cascade-rag pipeline.
- T11.1.3 Wire `cascade.search_codebase` to the code-aware index.
- T11.1.4 Daemon startup builds + injects the retriever; graceful "index not ready" only when truly unbuilt.
- T11.1.5 Integration test: index a fixture corpus, assert real hits + citations.

**E11.2 — True parallel orchestration (crate: cascade-agents)**
- T11.2.1 Implement real concurrency in `ChainStep::Parallel` via `futures::future::join_all` over `Arc<Mutex<ChainContext>>`.
- T11.2.2 Honor `SubGoal.parallel` in `CeoOrchestrator` (fan-out wave dispatch).
- T11.2.3 Concurrency cap (default `min(16, cores-2)`); excess queues.
- T11.2.4 Subagent-context template injected by `AgentExecutor` before each provider step (cache-stable prefix).
- T11.2.5 Tests: N concurrent children complete; wall-clock < sum; cap respected.

**E11.3 — Model tiers & context budget (crates: cascade-types, cascade-core, cascade-harness)**
- T11.3.1 `ModelRegistry`: `Tier → (provider_id, model_id)`, TOML-configurable, per-tier override.
- T11.3.2 Wire registry through `AgentSpec.model_pref` → `ProviderRouter::step`.
- T11.3.3 `TierConfig.on_demand` / `load_when` field; resolver keeps always vs deferred distinct.
- T11.3.4 Harness render emits on-demand rules as `->` pointer comments, not inlined bodies.
- T11.3.5 `cascade resolve --context-budget` strips deferred rules; warn over budget.
- T11.3.6 Tests: budget split honored; pointers render; registry resolves all four tiers.

EOP11 → tag `v0.9.1`, GitHub release.

---

## P12 — Content parity (`0.9.2`)

Author the behavioral rules + curated corpus as Cascade library entries, generated into harness files at the right tier with the always/on-demand discipline from P11.

**E12.1 — Behavioral rule library** (each as a `.cascade/library` rule, tier-scoped):
- destructive-deny-list (full ~40-pattern parity), autonomous-verification, output-conciseness-and-structure, context-efficiency, excellence-in-engineering, correspondence-human-tone, anti-drift, version-lock.

**E12.2 — Standards corpus** — port 12 per-language standards (typescript, rust, flutter, git, security, terminal-output, ai-app-architecture, …) as on-demand `load_when: coding/{lang}` rules.

**E12.3 — References corpus** — ~50 reference files seeded into `~/.cascade/knowledge/`, RAG-indexed at init.

**E12.4 — Providers corpus** — 8 service-knowledge files (cloudflare/github/hetzner/stripe/vercel/elastic-email/godaddy) as on-demand rules.

**E12.5 — `@import` expansion** in harness render; "reference up, never duplicate" lint (`cascade lint --no-duplicate`).

EOP12 → `v0.9.2`.

---

## P13 — Runtime integration (`0.9.3`)

Cascade stops being markdown-only and becomes a harness-config manager.

**E13.1 — settings.json management** — `cascade configure --harness <h>` writes permissions, `env` (e.g. `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE`), and the full deny[] array into `~/.claude/settings.json`; `claude-settings-enforce` equivalent (`cascade check settings`).

**E13.2 — CC hook registration** — atomic install of: SessionStart (inbox / x9-guard / index-warm), UserPromptSubmit (inbox-live), PreToolUse (RTK register + auto-approve `.claude/` writes), PostToolUse (telemetry), Stop. Extend the hook-write allow-list to the CC session events.

**E13.3 — File-ops parity** — ship `cascade write/edit` analogs or register the existing `claude-*` scripts; auto-approve hook for `.cascade/` writes.

EOP13 → `v0.9.3`. **This is the minimum-to-switch milestone** (generated CLAUDE.md is no longer a regression; deny-list + hooks actually apply; search works).

---

## P14 — Execution engine (`0.9.4`) — the largest phase

Port the PBD/PEWS autonomous Dev Shop into native Rust orchestrators.

**E14.1 — `/plan` engine** — 3-state machine (opening_gate→planning→ready_to_build), 15/15 completeness gate, carry-forward scan, 17-field contract forge (writer≠reviewer), `pbd-verify-ready` (contract + 950-agent ceiling + standing-authorizations).

**E14.2 — `/build` wave-loop** — per-wave dispatch, CR-A/B/C + QA-A/B/C sandwiches, sidecar checkpointing (wave-cursor / quota-pause), circuit breaker (3-fail halt), 5-cycle True-100%-Done gate, zero-user autonomy, resumability.

**E14.3 — Audit triad** — phase-opening (26-DQA + 8-SIEGE swarm), phase-closing (DQA+SIEGE+route-audit+ship sequence+residue), deep-qa (26-dim), SIEGE (8-vector). Wire the `ExternalChecks` / `OutboundSink` seams.

**E14.4 — Named agents** — code-reviewer (CR-B/CR-C modes), doc-writer, drift-detector, gap-scanner, onboarder as agent specs + chains.

**E14.5 — EOT/EOS/EOW/EOE/EOP runners**, `/status`, `/unblock`, atomic claim, step lifecycle.

EOP14 → `v0.9.4`. **This is where Cascade becomes *better* than local** (the swarm is native, GUI-observable, single-harness).

---

## P15 — Ops + memory refinement (`0.9.5`)

**E15.1 — Memory refinements** — typed frontmatter (name/description/tags/type), multi-corpus separation, **locked-dir privacy** (filename-only for health/VA threads — security-sensitive, must be airtight), curated semantic surface (description as a privileged RRF channel), feedback-capture discipline rule, `cascade recall` CLI, `cascade.memory.list` manifest.

**E15.2 — Cross-project inbox** — `cascade inbox send` CLI with project routing; SessionStart inbox check; archive-on-read.

**E15.3 — Credentials** — `cascade vault import` (vault.env → OS keychain); secrets browser distinct from note-vault.

**E15.4 — Backup/restore + health** — 3-layer backup (`cascade backup`/`restore`), external-process watchdog in the daemon supervisor.

**E15.5 — Fleet** — finish E12 migration: Claude Max usage polling + multi-account pooling into `QuotaStore`/`FleetWidget`.

EOP15 → `v0.9.5`.

### Kept external (Cascade registers/wraps, does not reimplement)
- **RTK** token proxy — registered as a PreToolUse hook (P13), binary stays external.
- **Telegram / remote-control** — undocumented CC plugin channels; revisit as a native notification subsystem only if prioritized.

---

## P16+ — Beyond parity (Cascade-only wins)

Capabilities local can't do, that justify the migration beyond mere replacement:
- **Live cross-harness sync** — one edit regenerates CC/OC/Codex/Cursor/Aider instantly (no symlinks, no dual GCI roots, no forked phase state).
- **Harness-agnostic policy** — one deny-list/policy set enforced across every harness, not maintained per-harness.
- **GUI for everything** — visual phase DAG editor, live agent-swarm view, memory browser, deny-list policy editor, RAG explorer (local is all markdown).
- **Semantic search over the instruction corpus itself** (local recall is lexical).
- **Cross-machine `.cascade` sync** via the daemon (local has rsync backup, not live sync).
- **WASM plugin marketplace** for community agents/rules/personas.
- **Telemetry/analytics** — agent success rates, token economics, phase velocity dashboards.

---

## Inbox-sourced roadmap additions (2026-06-15)

Folded in from the acamarata project inbox. Two coherent themes.

### Theme S — Security & prompt-injection hardening → **P13** (runtime integration), new epic **E13.4**
- **E13.4.1 Injection-aware UserPromptSubmit hook** (HIGH) — scan each incoming user message against an injection pattern set before any tool dispatch; on hit log + warn; on critical (direct deny-list-override attempt) halt the turn; sensitivity `strict|moderate|log-only` in settings. Cascade ships it as a default hook. The deny-list is currently the only guard under bypassPermissions — this adds a pre-dispatch gate.
- **E13.4.2 Deny-list injection audit** — cross-reference every deny-list pattern vs OWASP LLM Top-10 + published jailbreak/Pliny techniques; add encoding-variant coverage (base64/URL/unicode-homoglyph) and chained-command heuristics (sequences that individually pass but collectively violate); document in `deny-list-audit.md`. (Audit + heuristics, not just doc.)
- **E13.4.3 Agent prompt-size gate** — count tokens in the assembled agent system prompt at spawn; warn > 2000, error/block > 4000 (configurable `agent_prompt_max_warn|error`); emit top-3 largest sections on warn; per-agent-type telemetry. Pairs with E12 model-profile routing (right-size prompt AND model).
- **E13.4.4 RTK injection telemetry** (LOW) — scan intercepted Bash command strings for injection-ish patterns; log hits to a separate telemetry file (observability only, no blocking); weekly summary in `rtk gain --history`.

### Theme M — Model behavioral-profile routing → **P12**, extends the P11 model-tier registry
- **E12.x Behavioral-profile metadata + routing** — add per-model behavioral profile (defaultFormat, toolUseTrigger, refusalSensitivity, bestFor) to the model registry built in P11; route agent spawns by profile match, not tier alone (same tier ≠ same defaults across Claude/Gemini). Avoids formatting fights / spurious tool-use / refusal false-positives.

### External @acamarata packages Cascade consumes (separate repos — spin up via PCI)
- **`@acamarata/prompt-guard`** — zero-dep TS injection detection/sanitization (`scanPrompt`/`sanitizePrompt`, modes escape|block|tag). Cascade's hook (E13.4.1) consumes it.
- **`@acamarata/model-profiles`** — versioned LLM behavioral-profile data (`getProfile`/`getBestModelFor`). E12.x routing consumes it.

> The ~30 other inbox `audit-*` messages target *other* acamarata repos (ali/curtain/nrel/dart/python/npm) — out of scope for the cascade phase plan; left for their owning repos.

---

## Quality protocol (every phase)

1. Build agents work in isolated worktrees, crate-grouped to avoid conflicts.
2. Each ticket: implementation + rustdoc + co-located tests; files ≤300 lines, functions ≤50.
3. Per-crate gate in-worktree: `cargo build`, `cargo clippy -- -D warnings`, `cargo test`.
4. Integration gate on merge: full `cargo build --workspace`, `cargo deny check`, workspace tests.
5. EOP: bump patch, CHANGELOG entry, tag `v0.9.x`, GitHub release.
6. cargo env on every invocation: `CC=/usr/bin/cc CXX=/usr/bin/c++ CARGO_TARGET_AARCH64_APPLE_DARWIN_LINKER=/usr/bin/cc CARGO_INCREMENTAL=0` (avoids the cc-shadow hang + incremental-cache corruption).
7. No AI attribution in code or commits; commit messages avoid vendor names per the commit-msg hook.
