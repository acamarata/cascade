# Cascade — Complete Vision & Definition of Done (locked 2026-06-22)

The full target. Versions ship incrementally (each: merge → push → tag → GitHub
release + notes + bundles → README → wiki), but THIS is what "done" means.
Owner gave full authority to fill gaps with opinionated defaults; this doc records them.

## Identity (locked)
Standalone FOSS, lean, local-first **Claude Code extension** + multi-harness context
manager. Self-contained: single Rust binaries + Tauri + local SQLite. **No server,
no remote DB, no Docker** (anywhere — product or CI; CI uses native arch runners).

## Mac defaults (tier mapping)
- **Personal directory = `~/Downloads`** (personal scope — the threaded personal memory home).
- **Projects (APC) = `~/Sites`**.
- 6-tier cascade GCI(`~/.cascade`) → PCI → APC(`~/Sites`) → PPC → PRC → PAC.
- `discovery.rs` must default these on macOS (bind `~/Sites`→APC, recognize `~/Downloads` as the personal scope) without config. Other OSes use equivalent home-relative defaults.

## 1. Memory + Retrieval (perfect, infinite, reliable)
- **Storage = SQLite only**: `sqlite-vec` (vectors / pgvector-equiv) + **FTS5** (full-text / tsvector-equiv). No Postgres.
- **Embeddings**: fastembed/ONNX dense+sparse, **BGE-M3** target (config-swappable).
- **Retrieval**: 5-channel RRF (FTS, dense, sparse/curated, recency, ColBERT multivector) + cross-encoder rerank + citations.
- **Infinite memory**: append-only threaded memory (per-scope), auto-indexed incrementally; old threads stay retrievable via RAG, never truncated.
- **Privacy**: 2-layer scope-exclusion — locked dirs (medical/legal/financial/VA/custody/health/`~/Downloads` personal threads) **never** appear in any retrieval result; OS keychain for secrets. All local.
- **Personal-data store**: name/dob/family/etc. held locally in the personal scope, locked + excluded from shared retrieval, sensitive-firewalled (see §5).

## 2. PBD / PEWS (preserve in full — first-class)
The complete phased-development system, native in `cascade pbd`:
- Hierarchy: **Phase → Epic → Wave → Sprint → Ticket → Sub-ticket → Step**.
- Every ticket carries the full 17-field contract: AI instructions, **definition of done**, acceptance criteria, **checklists** (CR + QA), steps, references, docs-to-update, weight, model tier, deps, gates, sport_updates.
- Lifecycle commands: **EOT, EOS, EOW, EOE, EOP** (end of ticket/sprint/wave/epic/phase) — each verifies its unit, runs CR/QA gates, updates masters + docs, marks done. `cascade pbd {eot,eos,eow,eoe,eop}` + `/status` + `/unblock` + atomic claim + step lifecycle.
- State on disk (YAML), resumable, CR/QA scale by weight (CR-C/Opus for L/XL).

## 3. Coding standards (AI-aligned, opinionated, enforced)
Shipped as Cascade's behavioral-rule + standards library, enforced by `cascade doctor`/`health`:
- **≤300 lines/file**, **≤50 lines/function**; framework-native layout; opinionated to each framework's best practice.
- **DRY** (extract on 3rd copy) — every function written once; **3NF for data**, normalized for code.
- Modular + packaged: smallest reusable units; typed I/O (no `any`/`dynamic`); co-located tests; single responsibility.
- 6-field comment block on every reusable unit (Purpose/Inputs/Outputs/Constraints/Errors/Notes).
- Per-language standards (TS/Rust/Python/Go) + universal Git/Security. Docs updated per task (a task isn't done until docs are).

## 4. Version ladder (each = full release DoD)
- **v1.0** ✅ — lean release (resolver + harness-gen + RAG + MCP + plugins + CLI + import). SHIPPED.
- **v1.1** — Fleet: daemon poller loop (~1min) → `~/.cascade/quota-store.json` → menu-bar widget. Onboarding wired. PBD/PEWS completeness + coding-standards library completeness.
- **v1.2** — Model access + **sensitive-data firewall** + routing matrix (§5).
- **v1.3** — Beyond-parity: taxonomy/auto-classify, dynamic-learning rule (capture→GFP-tag→memory), semantic corpus search, GFP pre/post-prompt pipeline.
- **v1.4+** — Until the complete vision is fully realized: refine the baked-in behavioral instruction layer (§7), telemetry/analytics, plugin marketplace, GUI surfaces, and any remaining gaps. Keep shipping until done.
- `cascade update` tested between each (smooth, no hiccups).

## 7. Baked-in behavioral instruction layer (Cascade ships this — first-class)
Cascade is a context manager, so it must SHIP excellent default behavioral instructions
(a behavioral-rule library, generated into every harness's files) that make any agent
using Cascade operate to best practice. The library encodes:
- **Authorization & autonomy posture** — act-then-report within the user's standing
  authorization; full trust within scope; no permission-prompt loops; confirm only
  genuinely irreversible/outward actions. (Configurable per user; default = the
  operator's chosen posture, not hardcoded reckless.)
- **Anti-hallucination & anti-drift** — verify before claiming done (run the build/test);
  source-of-truth files beat conversation; never invent features not in the spec; flag
  missing specs/contradictions instead of guessing; name things precisely.
- **Firm vision/mission awareness** — hold the high-level VISION/FEATURES/mission at all
  times; keep strictly in scope and on task; re-read the active plan after compression.
- **Dynamic learning** — capture discoveries/decisions/lessons to memory as you go
  (decisions / lessons / patterns); use the cheap pool (GFP) to tag/classify; recall
  before re-deriving.
- **Delegation & model discipline** — top-tier plans/reviews, cheaper tiers execute;
  route per the §5 matrix; max free/cheap quotas for adversarial CR/QA.
These ship as `tier = "any"` behavioral templates + are enforceable via `cascade doctor`.

## 5. Model access + routing matrix (v1.2 core)
**Primary = the native Claude account in Claude Code (T0)** — reserved for interactive chat/final synthesis, used sparingly.
Additional sources made available as `model:` entries / MCP / skills that launch each tool's CLI:
- **Extra Claude accounts (acc2, acc3…)** — via Claude Code CLI + **smithers/claude-p (PTY)** (only Claude needs PTY). **Drained FIRST to exhaustion** (T0 main is reserved for chatting).
- **Codex / ChatGPT** — `codex exec` (CLI `-p`, headless).
- **Google Pro (Gemini AGY)** — `agy -p` (CLI headless).
- **GFP (Gemini Free Pool, free Flash)** — key pool, round-robin. **Maxed for free work** (pre/post-prompt, taxonomy, classification, cheap research).
- **OpenCode-Go** — `opencode run` (headless), dollar-metered.

**Quota-aware launching**: the 1-min `quota-store.json` (per-source windows) + budget-guard → before any dispatch, pick the source WITH headroom per the matrix.

**Routing matrix (opinionated default — fill/maximize all quotas):**
| Task class | Preferred source order |
|---|---|
| T0 interactive chat / final synthesis | main Claude (reserved) |
| Bulk execution / drafting | acc2+ Claude (PTY, drain first) → Codex → OC-Go |
| Cheap / trivial / pre-post-prompt / taxonomy / classify | **GFP free Flash** (max it) → local LLM |
| Adversarial research / CR / QA | **cross-family** (different family than author) — GFP + agy + OC-Go + Codex, maximize free/cheap quotas |
| Hard / final correctness gate | main Claude Opus |
| **Sensitive (PII / VA / custody / health / personal)** | **Claude or local ONLY — never external model, never synced (FIREWALL)** |

PTY-first: subscription limits primary; **paid-API overage opt-in and OFF by default**. Sub-pooling stays **PRIVATE / feature-gated** (public npm never advertises it; ToS-gray).

## 6. Final acceptance
- Fully installed + 100% running on the owner's machine (CLI + daemon + menu-bar app + widget).
- Onboarding/import migrates the owner's real `~/.claude` losslessly (verified 100% — done).
- Cross-checked against `~/.claude-backup/2026-06-21-220703` (3.9G) — nothing lost.
- Owner restarts Claude.app at the end; Cascade then provides all the above.
- All versions v1.0–v1.3 released to GitHub with bundles + notes; `cascade update` path verified.
