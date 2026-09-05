<!-- cascade:generate-instructions digest=sha256:<DIGEST> -->
## Cascade Context — GCI Tier (Global Cascade Instructions)

**MCP server:** `stdio: cascade mcp stdio`

Call `cascade.search` before responding to queries about this project.
Call `cascade.context_slice` to retrieve relevant context from the RAG index.
If the cascade MCP tools are unavailable, run `cascade recall` and `cascade context slice` through Bash instead.

# Global Cascade Instructions (GCI)

**Tier:** GCI — applies to ALL work across ALL projects, coding and non-coding.
**Sibling files:** `AGENTS.md` and `CLAUDE.md` are symlinks to this file. Edit only `CASCADE.md`.

---

## Cascade Hierarchy

Five-tier cascade: **GCI → PCI → APC → PPC → PRC → PAC**. Higher tiers always win on conflicts.

| Tier | Scope | Location |
|---|---|---|
| **GCI** | All projects, all AI work | `~/.cascade/CASCADE.md` |
| **PCI** | All coding projects under a root dir | `<PROJECTS_ROOT>/.cascade/CASCADE.md` |
| **APC** | One multi-repo product | `<PRODUCT_ROOT>/.cascade/CASCADE.md` |
| **PPC** | One project (single repo or workspace) | `<PROJECT_ROOT>/.cascade/CASCADE.md` |
| **PRC** | One repository | `<REPO_ROOT>/.cascade/CASCADE.md` |
| **PAC** | One app within a multi-app repo | `<REPO_ROOT>/<APP>/.cascade/CASCADE.md` |

**Sibling symlink rule:** every `.cascade/CASCADE.md` has `AGENTS.md` and `CLAUDE.md` siblings that are symlinks to it. Both harnesses read the same content. Edit only `CASCADE.md`; the symlinks auto-track.

```bash
# Create siblings at any tier
ln -s CASCADE.md .cascade/AGENTS.md
ln -s CASCADE.md .cascade/CLAUDE.md
```

---

## Orchestration Doctrine

**The top-level context plans and reviews. Executor agents implement. Never hoard bulk work in the top context.**

### Agent Tiers

| Tier | Role | When |
|---|---|---|
| **T0** | Observer / Orchestrator — holds vision, plans, dispatches, reviews | Top context only |
| **T1** | Planner / Decider — architecture decisions, adversarial review, final acceptance gates | Surgical; justification required |
| **T2** | Executor — bulk implementation, code writes, CR-A/B, QA-A/B, doc drafts | Default for all subagents |
| **T3** | Cheap / Triage — classification, extraction, labeling, ≤10-line structured output | High-volume sweeps |

### Three-Call Hard Cap

If a task takes more than 3 tool calls in the top context, spawn an agent for the rest. T0 should feel light: planning, reviewing, reporting.

### Dispatch Gate (ask in order; first yes wins)

1. Output ≤10 lines OR structured (JSON/yes-no/labels/counts)? → **T3**
2. Bulk execution / code writes / CR / QA / doc drafts / research? → **T2** (default)
3. Architecture review / adversarial / final acceptance on mission-critical? → **T1** (with written justification)

### N-Way Parallelism

N independent units → N parallel agents. No upper bound on T2/T3 count. T3 hundreds acceptable. T2 dozens per wave.

**Adversarial Homework Rule:** the agent that writes code must not be the agent that reviews it. CR and QA agents are always independent of the implementing agent.

**Full rule:** `.cascade/rules/orchestrator-executor-split.md`

---

## Behavioral Doctrine

### Session Start

1. Read canonical state files (`.cascade/phases/`, SPORT, tasks)
2. Check `.cascade/inbox/` for new messages
3. Review `.cascade/ideas/` for items needing triage
4. Read `.cascade/memory/` for relevant context
5. Write `.cascade/temp/.session-context.json` with agent ID and task

### Memory Protocol

| File | Update when |
|---|---|
| `.cascade/memory/decisions.md` | Any significant technical choice |
| `.cascade/memory/lessons.md` | A gotcha or mistake is discovered |
| `.cascade/memory/patterns.md` | A codebase convention is established |

**User feedback → memory (Hard Rule):** every correction, preference, or behavioral feedback is written to a memory file before responding further. Acknowledge in chat only after persisting.

### Ideas Capture

Every idea — from any source — goes into `.cascade/ideas/{slug}.md` immediately. No idea is too small. Format: title, source, date, status (raw/exploring/queued/deferred/rejected), description.

### Anti-Drift Rules

- Never invent features. Not in FEATURES.md = does not exist.
- Re-read canonical state after every context compression.
- Source-of-truth files always win over conversation context.
- Verify before marking done. Run the build, check the test.
- Write discoveries to `.cascade/memory/` immediately.

**Full rule:** `.cascade/rules/anti-drift.md`

---

## Engineering Quality Doctrine

### In-Code Comment Blocks (Hard Rule)

Every reusable unit (function, hook, util, component, module, plugin) carries a comment block:

```
Purpose: <one-line what this does>
Inputs:  <param signature + invariants>
Outputs: <return shape + side-effect surface>
Constraints: <non-obvious WHY — rate limits, perf bounds, security>
SPORT:   <pointer to master list entry, e.g. F-AUTH-12>
```

### File and Function Size

- No file exceeds 500 lines. Split by domain at that threshold.
- No function exceeds 50 lines. Decompose if larger.

### Typed Boundaries

Every reusable unit boundary is typed. No `any`/`dynamic`/`interface{}` without a comment explaining why.

### Tests

Co-located (`foo.ts` + `foo.test.ts`) or mirror-structured (`__tests__/foo.test.ts`). Cover happy path, edge cases, and failure modes.

### Master Lists (Hard Rule)

Maintain exhaustive master lists at `.cascade/docs/`. Before implementing: check the list. After implementing: update immediately.

**Full rule:** `.cascade/rules/excellence-in-engineering.md`

---

## Process and Phase Doctrine

### PEWS Hierarchy

```
Phase → Epic → Wave → Sprint → Ticket → Sub-Ticket
```

- **Epic:** domain/planning concept (user-visible capability)
- **Wave:** parallelism batch (all sprints dependency-free by construction)
- **Sprint:** coherent shippable body (~2–8 tickets)
- **Ticket:** bounded implementation unit (weight: XS/S/M/L/XL)

### Phase Lifecycle

`planning` → `ready_to_build` → `building` → `closing_gate` → `done`

State lives in YAML files under `.cascade/phases/`. Both harnesses read and write the same directory.

### Pre-Build Zero-Blocker Gate (Hard Rule)

Build mode must never be invoked with any blocker present. Resolve all blocked tickets, pending decisions, and missing credentials in Plan phase before flipping to `ready_to_build`.

### Build Mode Autonomy (Hard Rule)

During build phase: never pause for "Continue?" prompts between waves. Dispatch the next wave immediately. Only valid stops: True-100%-Done, circuit-breaker (5 iterations without convergence), destructive-deny-list action, missing vault credential, or explicit user stop.

### True-100%-Done Gate

A ticket is done only when: compiles, tests pass, lint clean, CR complete, QA complete, no stubs (`TODO/FIXME/placeholder`), docs updated, SPORT updated.

**Full rule:** `.cascade/rules/phase-based-development.md`

---

## Hard Rules Index

| Rule | File | Trigger |
|---|---|---|
| Orchestrator-Executor Split | `.cascade/rules/orchestrator-executor-split.md` | Any multi-step task |
| N-Way Parallelism | `.cascade/rules/parallelism-unlimited.md` | N independent units |
| In-Code Comment Blocks | `.cascade/rules/excellence-in-engineering.md` | Every new reusable unit |
| Master Lists | `.cascade/rules/excellence-in-engineering.md` | Every task completion |
| Anti-Drift | `.cascade/rules/anti-drift.md` | Any ambiguity detected |
| Pre-Build Zero-Blocker | `.cascade/rules/phase-based-development.md` | Before `ready_to_build` |
| Build Mode Autonomy | `.cascade/rules/build-mode-autonomous.md` | During build phase |
| Destructive Deny-List | `.cascade/rules/destructive-deny-list.md` | Before any destructive op |
| Memory Capture | `.cascade/rules/memory-protocol.md` | User feedback received |
| Human Writing Style | `.cascade/rules/human-writing-style.md` | Any user-facing content |
| No AI Attribution | `.cascade/rules/no-ai-attribution.md` | Before any commit |

---

## File Operations

| Operation | Tool | Never use |
|---|---|---|
| Read a file | `Read` tool | `cat`, `head`, `tail` |
| Write a new file | `Write` tool | `echo >`, heredocs |
| Edit a file | `Edit` tool | `sed`, `awk` |
| Create/overwrite `.cascade/` file | `cascade-write` script | `Write` tool on `.cascade/` paths |
| Edit `.cascade/` file | `cascade-edit` script | `Edit` tool on `.cascade/` paths |

`.cascade/` is AI working memory — gitignored. Use `cascade-*` scripts to avoid harness permission prompts.

---

## Writing Quality

All user-facing content reads like a human wrote it.

**Never use:** em dashes (—) as connectors · "dive into / delve into" · "seamlessly" · "robust / comprehensive / powerful" · "leverage" (use "use") · "moreover / furthermore / additionally" · exclamation marks in docs · passive voice stacking.

**Em dash rule:** replace `X — Y` with a period, comma, or colon.

**Full rule:** `.cascade/rules/human-writing-style.md`

---

## No AI Attribution (Hard Rule)

Never add AI attribution to version-controlled output: no `Co-Authored-By`, no "Generated by" comments, no AI tool references in PRs, docs, or code.

<!-- /cascade:generate-instructions -->
