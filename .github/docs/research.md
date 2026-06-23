# AI Models & Rates — Cascade Research
(date: 2026-06-01)

---

## 1. Anthropic / Claude Code (CC) Harness

### Current Models

| Model | API ID | Context | Input ($/MTok) | Output ($/MTok) | Tier |
|---|---|---|---|---|---|
| Claude Opus 4.8 | `claude-opus-4-8` | 1M tokens (200K on MS Foundry) | $5 standard / $10 fast / $2.50 batch | $25 standard / $50 fast / $12.50 batch | T1 (surgical) |
| Claude Sonnet 4.6 | `claude-sonnet-4-6` | 1M tokens | $3 standard / $1.50 batch | $15 standard / $7.50 batch | T2 (workhorse) |
| Claude Haiku 4.5 | `claude-haiku-4-5-20251001` (alias: `claude-haiku-4-5`) | 200K tokens | $1 standard / $0.50 batch | $5 standard / $2.50 batch | T3 (cheap/triage) |

**Deprecated / Retiring:**
- `claude-opus-4-20250514` (original Claude Opus 4) — retires June 15, 2026
- Claude Sonnet 4 — retires June 15, 2026

**Knowledge cutoffs:** Opus 4.8: Jan 2026 | Sonnet 4.6: Aug 2025 | Haiku 4.5: Feb 2025

**Max output tokens:** Opus 4.8: 128K sync / 300K via Batch API (with `output-300k-2026-03-24` beta header) | Sonnet 4.6: standard | Haiku 4.5: 64K

**New tokenizer note:** Opus 4.7 and 4.8 use a new tokenizer that consumes up to 35% more tokens than Opus 4.6 for identical text. Per-token price is unchanged but effective cost per request can be higher.

---

### Claude Max $200/mo Plan — Quota Structure

**Plan tiers:**
- Max 5x: $100/month — 5x Pro usage per session
- Max 20x: $200/month — 20x Pro usage per session (~900 messages per 5-hour rolling window)

**Quota mechanics:**
- Two separate weekly limits: (1) across all models combined, (2) Sonnet-specific
- Both reset 7 days after session start (rolling, NOT calendar week)
- 5-hour rolling window for per-session message count
- Claude Code and claude.ai chat share the SAME quota pool (same 5-hour window)
- On CC session quota hit: ALL models become unavailable simultaneously (including Haiku) — no T3 workaround like OC has

**Opus quota bucket:** Opus has a SEPARATE, SCARCER weekly quota bucket from Sonnet. This is confirmed by Anthropic support docs. Protect Opus quota; burn Sonnet liberally.

**Observed utilization (across 4 accounts):** Sonnet consistently at 10-30% utilized at week-end per `claude-usage week`. Opus is meaningfully tighter.

**4-account strategy:**
- 4 accounts × $200/mo = 4 independent quota pools
- Each VS Code window bound to one account has independent quota
- Priority rotation: `claude-best-account` chain: acct4 → acct3 → acct2 → acct1 (lowest utilization first)
- `/aa` dashboard shows live per-account utilization and renewal countdown

**Prompt caching:**
- 5-min cache: 1.25x write cost, 0.1x read cost (pays off after 1 cache read)
- 1-hour cache: 2.0x write cost, 0.1x read cost (pays off after 2+ reads)
- Batch API: 50% discount on input + output (stackable with caching)

---

### T0/T1/T2/T3 Tier Assignments (CC Harness)

| Tier | Role | CC Model | Effort | Notes |
|---|---|---|---|---|
| T0 (standing) | Observer / everyday orchestration | Sonnet 4.6 · Medium | medium | Claude.app default. Personal ops, triage, most sessions. |
| T0 (heavy) | Observer / swarm-heavy sessions | Opus 4.8 · Fast · Medium | medium → high | Claude2.app for Build/Plan kickoffs. Load-balances Opus bucket vs Sonnet during swarm-heavy work. |
| T1 | Planner / arch decisions / CR-C | Opus 4.8 | high (default); xhigh for deepest adversarial | Written justification required. Surgical only. |
| T2-plan | Code review / sprint planning / QA-B | Sonnet 4.6 | medium | Peer CR, sprint planning, QA integration. |
| T2-exec | Bulk ticket execution | Sonnet 4.6 | medium | All ticket implementation, doc drafts, scoped research. |
| T2-code | Pure coding / refactors | Sonnet 4.6 | medium | Code-heavy tickets with no research component. |
| T3 | Cheap / triage | Haiku 4.5 | low | Classification, labeling, extraction, ack-only, QA-A smoke. Hundreds of parallel calls acceptable. |

---

### Claude.app vs Claude2.app

| App | Default Model | Effort | Scope |
|---|---|---|---|
| **Claude.app** | Sonnet 4.6 | Medium | Downloads/personal ops — email, VA, research, CamClaw diagnostics, triage |
| **Claude2.app** | Opus 4.8 · Fast ON | Medium → High | ASI → coding PPIs — Plan phase kickoff, Build orchestration, CR-C, closing_gate synthesis |

**Rule:** Personal / Downloads → Claude.app. Coding / PEWS Build+Plan → Claude2.app (or OpenCode).

Never switch model mid-session — destroys the shared prompt-cache prefix. Pick the app at session start; the app remembers the model.

---

### Opus 4.8 Fast Mode

**What it is:** Inference speed tier for Opus 4.8. ~2.5x faster output tokens/second. ~3x cheaper than prior Opus fast mode pricing ($10/$50 per MTok vs $30/$150 for Opus 4.6/4.7 fast).

**How to enable:**
- In CC UI (interactive sessions): `/fast` slash command, OR toggle the Fast mode switch in the model picker
- In API: `speed: "fast"` parameter + beta header `"fast-mode-2026-02-01"` in `client.beta.messages.create()`
- **NOT** a `settings.json` key — UI toggle or API beta header only

**Fast mode caveats:**
- Research preview status — requires waitlist access
- NOT available on Vertex AI, Bedrock, or Microsoft Foundry
- NOT available with Batch API or Priority Tier
- Switching between fast and standard **invalidates the prompt cache** — requests at different speeds do not share cached prefixes
- Fast mode has a separate rate limit bucket from standard Opus with its own 429 headers
- Implication: if using fast mode for T0 (Opus), all subagent prompts sharing the cached prefix must be consistent speed

```python
# API fast mode example
client.beta.messages.create(
    model="claude-opus-4-8",
    max_tokens=4096,
    speed="fast",
    betas=["fast-mode-2026-02-01"],
    messages=[{"role": "user", "content": "..."}]
)
```

---

### Effort Levels (CC)

| Level | Available on | Notes |
|---|---|---|
| `low` | Opus 4.x, Sonnet 4.6 | Fast classification, triage, structured output |
| `medium` | Opus 4.x, Sonnet 4.6 | Recommended default for most agentic/coding |
| `high` | Opus 4.x, Sonnet 4.6 | Opus 4.8 default on all surfaces including CC |
| `xhigh` | Opus 4.7, Opus 4.8 ONLY | Deepest adversarial/arch sessions. **NOT available on Sonnet 4.6.** |
| `max` | Opus 4.x, Sonnet 4.6 | Rarely worth ~2x xhigh cost |

In API: `output_config: {"effort": "medium"}` (NOT a top-level parameter).

In CC subagent frontmatter: `effort: medium`

In Agent tool calls (no structured parameter): pass as prompt directive text:
```
"Use medium effort — this is routine bulk execution."
"Apply xhigh effort — this is a CR-C architecture review on critical-path auth changes."
"Use low effort — classify these 50 items, return JSON only."
```

---

### CC Agent / Subagent Dispatch

**Subagent file format (YAML frontmatter + markdown body):**
```markdown
---
name: ticket-executor
description: Implements a single PEWS ticket. Use for bulk M/L/XL tickets.
tools: Read, Write, Edit, Bash, Glob, Grep
model: sonnet
effort: medium
background: false
isolation: worktree
color: blue
maxTurns: 50
---

You are a senior engineer implementing a PEWS ticket exactly as specified. Write tests. No stubs.
```

**All supported frontmatter fields:**

| Field | Values | Notes |
|---|---|---|
| `name` | lowercase-hyphenated string | Required. Unique identifier. |
| `description` | string | Required. When Claude auto-delegates to this agent. |
| `tools` | Read, Write, Edit, Bash, Glob, Grep, Agent, Agent(type1,type2), ... | Allowlist. Inherits all if omitted. |
| `disallowedTools` | same set | Denylist. Applied before tools allowlist. |
| `model` | `sonnet`, `opus`, `haiku`, full ID like `claude-opus-4-8`, `inherit` | Defaults to `inherit` (parent session model). |
| `effort` | `low`, `medium`, `high`, `xhigh`, `max` | Overrides session effort. `xhigh` only on Opus 4.7/4.8. |
| `background` | `true` / `false` | `true` = always run as background task. |
| `isolation` | `worktree` | Isolated git worktree copy. Auto-cleaned if no changes. |
| `permissionMode` | `default`, `acceptEdits`, `auto`, `dontAsk`, `bypassPermissions`, `plan` | Ignored in plugins. |
| `maxTurns` | integer | Max agentic turns before stop. |
| `memory` | `user`, `project`, `local` | Persistent memory dir. |
| `mcpServers` | list | Scoped to subagent. |
| `color` | red, blue, green, yellow, purple, orange, pink, cyan | UI display color. |

**Model resolution order (per invocation):**
1. `CLAUDE_CODE_SUBAGENT_MODEL` env var
2. Per-invocation `model` parameter passed by Claude when it delegates
3. Subagent definition's `model` frontmatter field
4. Main conversation's model

**Subagents CANNOT spawn other subagents** (no nesting). The `Agent` tool is not available inside subagents.

**Dynamic Workflows (Opus 4.8, May 2026):** CC can coordinate up to 1,000 total agents per run, with up to 16 concurrent. Used for Plan phase epic decomposition and closing_gate DQA/SIEGE sweeps. Wave boundary = 1 workflow run. One workflow RUN = one PEWS WAVE.

**CLI-defined subagents (session-only, no file):**
```bash
claude --agents '{
  "ticket-executor": {
    "description": "Implements a single PEWS ticket.",
    "prompt": "You are a senior engineer. Implement the ticket spec exactly. No stubs.",
    "tools": ["Read", "Write", "Edit", "Bash", "Glob", "Grep"],
    "model": "sonnet",
    "effort": "medium"
  },
  "triage-agent": {
    "description": "Classifies items into buckets. Returns JSON only.",
    "prompt": "Classify each item. Return structured JSON. No prose.",
    "tools": ["Read"],
    "model": "haiku",
    "effort": "low"
  }
}'
```

**Recommended subagent frontmatter patterns:**

T3 Haiku — triage/classification:
```yaml
model: haiku
effort: low
tools: Read, Glob, Grep
background: false
```

T2 Sonnet — bulk execution:
```yaml
model: sonnet
effort: medium
tools: Read, Write, Edit, Bash, Glob, Grep
isolation: worktree
```

T2 Sonnet — SIEGE attack vector:
```yaml
model: sonnet
effort: high
tools: Read, Bash, Glob, Grep
```

T1 Opus — CR-C / arch review (surgical, written justification required):
```yaml
model: opus
effort: xhigh
tools: Read, Glob, Grep, Bash
maxTurns: 20
```

**Prompt cache discipline:** Every subagent prompt MUST begin with the canonical context block from `~/.claude/templates/subagent-context.md`. This makes N parallel agents cost ~1x on the prefix instead of Nx. NEVER switch models mid-session.

**CC session launch (via `~/bin/cc` wrapper):**
```bash
# Standard daily driver (Sonnet T0, medium effort) — Claude.app
cc                           # Downloads/personal ops
cc --effort high             # Bump effort for specific session

# Heavy coding session (Opus T0, fast mode) — Claude2.app
cc --model opus --effort xhigh   # Plan phase kickoff
cc --model opus              # CR-C arch review (high effort = default)
```

---

## 2. OpenAI / Codex + OpenCode (OC) Harness

### Current OpenAI Models

| Model | API ID | Context | Input ($/MTok) | Output ($/MTok) | Tier |
|---|---|---|---|---|---|
| GPT-5.5 | `gpt-5.5` | 1,050K tokens | $5.00 ($0.50 cached) | $30.00 | T1 (OC planner/arch) |
| GPT-5.5 Pro | `gpt-5.5-pro` | 1,050K tokens | $30.00 | $180.00 | T1-max (deepest batch reasoning) |
| GPT-5.4 | `gpt-5.4` | 1,100K tokens | $2.50 ($0.25 cached) | $15.00 | T1/T2 (strong T1 substitute at lower cost) |
| GPT-5.4 mini | `gpt-5.4-mini` | 400K tokens | $0.75 | $4.50 | T2-fast (fast bulk, computer use) |
| GPT-5.3-Codex | `gpt-5.3-codex` | 400K tokens | $1.75 ($0.175 cached) | $14.00 | T2-code (agentic coding specialist) |
| GPT-5.2-Codex | `gpt-5.2-codex` | 400K tokens | $1.75 ($0.175 cached) | $14.00 | T2-code legacy (use 5.3 instead) |
| GPT-5.1-Codex | `gpt-5.1-codex` | 400K tokens | $1.25 ($0.125 cached) | $10.00 | T2-code older |
| o3 | `o3` | 200K tokens | $2.00 | $8.00 | T1-reasoning (hard reasoning, cheaper than GPT-5.5) |
| o4-mini | `o4-mini` | 200K tokens (100K max output) | $1.10 ($0.275 cached) | $4.40 | T2-reasoning (efficient reasoning for coding/review) |
| GPT-4.1 | `gpt-4.1` | 1,000K tokens | $2.00 | $8.00 | T2-longctx (long-context doc analysis) |
| GPT-4.1 Nano | `gpt-4.1-nano` | 1,000K tokens | $0.10 ($0.025 cached) | $0.40 | T3 (cheapest OpenAI with 1M context) |
| GPT-5 Nano | `gpt-5-nano` | 400K tokens | $0.05 ($0.005 cached) | $0.40 | T3 (cheapest GPT-5 gen model) |
| GPT-4o | `gpt-4o` | 128K tokens | $2.50 ($1.25 cached) | $10.00 | T2-legacy (superseded by GPT-5.4) |
| GPT-4o-mini | `gpt-4o-mini` | 128K tokens | $0.15 ($0.075 cached) | $0.60 | T3-legacy (superseded by GPT-5-Nano) |
| o1 | `o1` | 200K tokens | $15.00 ($7.50 cached) | $60.00 | T1-legacy (avoid; use o3 instead at 7.5x lower cost) |

**Notes on reasoning models (o-series):** Reasoning tokens are billed as output tokens. A simple-looking response may consume 5-10x the visible token count. Budget accordingly.

**GPT-5.5 snapshot:** `gpt-5.5-2026-04-23`. Knowledge cutoff: Dec 1, 2025. Released April 23, 2026.

**GPT-5.3-Codex:** Released Feb 24, 2026. 25% faster than prior Codex. Supports `low/medium/high/xhigh` reasoning effort settings. This is the canonical T2-code model in OpenCode.

**"Fast mode" in Codex (important clarification):** Fast mode is a Codex-UI feature (1.5x faster, 2.5x cost, 400K context window). It is a UI toggle — NOT a distinct API model string. There is no `gpt-5.5-fast` API ID. For API use, use `gpt-5.5` (standard) or `gpt-5.5-pro` (extended reasoning). The GCI references `gpt-5.5-fast` in the PEWS harness table — this should be corrected to `gpt-5.4-mini` for T2-fast workloads.

---

### OpenCode (OC) Harness — What It Is

OpenCode is an open-source, MIT-licensed terminal coding agent built in Go by the SST/Anomaly team. It is a **separate harness** from Claude Code — an alternative terminal-based agentic coding tool that runs alongside CC. CC and OC share the same on-disk `.claude/` state (phases, tickets, memory, docs, inbox) via AGENTS.md symlinks. Only model names and provider routing differ.

OpenCode uses the Vercel AI SDK and Models.dev catalog to support 75+ LLM providers.

**Config file:** `~/.config/opencode/opencode.json`

**Auth:** `/connect` command handles provider authentication interactively, or set env vars directly.

---

### What "Go" Means in OpenCode Context

**CRITICAL CLARIFICATION:** "Go" in the OC context is NOT the Go programming language and NOT GPT-4o. It is **OpenCode's own $10/month subscription plan** for open-source models.

| Subscription | Price | Provider ID | Model format | Usage limits |
|---|---|---|---|---|
| **OpenCode Go** | $5 first month, then $10/mo | `opencode-go` | `opencode-go/<model-id>` | $12/5hr, $30/week, $60/month |
| **OpenCode Zen** | Pay-as-you-go, $20 min top-up | `opencode` | `opencode/<model-id>` | None (pay per token) |
| **OpenCode Black** | Enterprise | — | — | Not for personal use |

Go models (all from Chinese AI labs): GLM-5, GLM-5.1, Kimi K2.5, Kimi K2.6, DeepSeek V4 Pro, DeepSeek V4 Flash, MiMo-V2.5, MiMo-V2.5-Pro, MiniMax M2.5/M2.7/M3, Qwen 3.6 Plus, Qwen 3.7 Max.

OpenCode Go can be used with Claude Code or any OpenAI-compatible agent — not just OpenCode itself.

Zen is the overflow/premium tier: curated models tested by the OpenCode team, including Claude, GPT, Gemini models.

---

### OC Tier Assignments

| Tier | Role | OC Model | Model ID | Notes |
|---|---|---|---|---|
| T0 | Observer / orchestration | GFP (gemini-2.5-flash) | via Gemini free pool | OC uses GFP pool (28 keys, 560 RPD). |
| T1 | Planner / arch decisions | GPT-5.5 | `openai/gpt-5.5` | Surgical with justification. T0 and T1 share same model in OC. |
| T2-plan | Code review / sprint planning / QA-B | Kimi K2.6 | `opencode-go/kimi-k2.6` | 2 concurrent max. Fallback: Qwen 3.6 Plus. |
| T2-exec | Bulk ticket execution | DeepSeek V4 Pro | `opencode-go/deepseek-v4-pro` | 3 concurrent. Primary OC workhorse. ~3,300 req/5hr. |
| T2-code | Pure coding / refactors | GPT-5.3-Codex | `openai/gpt-5.3-codex` | Code-only tasks with no research component. |
| T2-fast | Mid-tier fast bulk | GPT-5.4-mini | `openai/gpt-5.4-mini` | Replaces the non-existent `gpt-5.5-fast` ID. |
| T4 | Extended thinking / deep research | GPP (gemini-3.1-pro) | `google/gemini-3.1-pro-preview` | OC only. 1M context. SPORT/Docs/Wiki. |
| T3 | Cheap / triage | GFP or DeepSeek V4 Flash | `opencode-go/deepseek-v4-flash` | GFP pool covers T3. V4 Flash = ~31,650 req/5hr in Go. |
| T3-free | Free-tier fallback (OC quota hit) | DeepSeek V4 Flash | `opencode-go/deepseek-v4-flash` | OC primary quota hit: continue XS/S tickets via V4 Flash. |

---

### OpenCode — OpenAI Provider Configuration

**Global config (`~/.config/opencode/opencode.json`):**
```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "openai/gpt-5.5",
  "small_model": "openai/gpt-5-nano",
  "provider": {
    "openai": {
      "options": {
        "apiKey": "{env:OPENAI_API_KEY}"
      }
    }
  },
  "agent": {
    "orchestrator": {
      "mode": "primary",
      "description": "T0/T1 planning and dispatch",
      "model": "openai/gpt-5.5"
    },
    "executor": {
      "mode": "subagent",
      "description": "T2-code pure coding tickets",
      "model": "openai/gpt-5.3-codex"
    },
    "reviewer": {
      "mode": "subagent",
      "description": "T2-plan code review and sprint planning",
      "model": "openai/o4-mini"
    },
    "fast-executor": {
      "mode": "subagent",
      "description": "T2-fast bulk routine tasks",
      "model": "openai/gpt-5.4-mini"
    },
    "triage": {
      "mode": "subagent",
      "description": "T3 classification and extraction",
      "model": "openai/gpt-5-nano"
    }
  }
}
```

**Using OpenCode Zen (`opencode/` prefix):**
```json
{
  "model": "opencode/gpt-5.5",
  "agent": {
    "executor": { "model": "opencode/gpt-5.3-codex" },
    "fallback": { "model": "opencode/deepseek-v4-pro" }
  }
}
```

**OpenCode Zen available OpenAI models (via `opencode/` prefix):**
- `opencode/gpt-5-nano` ($0.05/$0.40)
- `opencode/gpt-5.4-nano` ($0.20/$1.00)
- `opencode/gpt-5.1` ($0.625/$5.00)
- `opencode/gpt-5.1-codex` ($1.25/$10.00)
- `opencode/gpt-5.3-codex` ($1.75/$14.00)
- `opencode/gpt-5.4` ($2.50/$15.00)
- `opencode/gpt-5.5` ($5.00/$30.00)
- `opencode/gpt-5.5-pro` ($30.00/$180.00)

**Effort levels for OpenAI models in OC:** GPT-5.3-Codex, GPT-5.5, GPT-5.4 all support `none/low/medium/high/xhigh`. Pass as prompt directive since Agent tool has no effort parameter.

**Codex subscription vs API access:** OpenAI Codex (the product) is included in ChatGPT Plus ($20/mo), Pro ($200/mo), Business ($30/user/mo). There is NO standalone Codex subscription. For API access (what OC uses), pay-as-you-go token pricing applies regardless of ChatGPT subscription tier. OpenCode Zen and OpenCode Go are separate billing from OpenAI direct API.

---

## 3. Google Gemini — Antigravity + OC + Generic Usage

### Current Gemini Models

| Model | API ID | Context | Input ($/MTok) | Output ($/MTok) | Free Tier | Notes |
|---|---|---|---|---|---|---|
| Gemini 3.5 Flash | `gemini-3.5-flash` | 1M tokens | $1.50 | $9.00 | None | Most capable Flash; launched 2026-05-19 |
| Gemini 3.1 Flash-Lite | `gemini-3.1-flash-lite` | 1M tokens (64K output) | $0.25 | $1.50 | 15 RPM / 1,500 RPD / 250K TPM | **Recommended GFP T3 replacement for deprecated 2.0 Flash** |
| Gemini 3.1 Pro Preview | `gemini-3.1-pro-preview` | 1M tokens (64K output) | $2.00 (≤200K ctx) / $4.00 (>200K) | $12.00 | None | Paid only. T1-equivalent for OC. |
| Gemini 3 Flash Preview | `gemini-3-flash-preview` | 1M tokens (64K output) | ~$0.50 (est.) | ~$3.00 (est.) | 10 RPM / 1,500 RPD / 250K TPM | Preview status. Dynamic thinking. |
| Gemini 2.5 Flash | `gemini-2.5-flash` | 1M tokens | $0.30 | $2.50 | 10-15 RPM / 250-1,500 RPD / 250K-1M TPM | Stable GA. Best price-performance T2/T3. |
| Gemini 2.5 Flash-Lite | `gemini-2.5-flash-lite` | 1M tokens | $0.10 | $0.40 | 15-30 RPM / 1,000-1,500 RPD / 250K-1M TPM | Cheapest 2.5. T3 cheap sweeps. |
| Gemini 2.5 Pro | `gemini-2.5-pro` | 1M tokens (2M on Vertex AI) | $1.25 (≤200K) / $2.50 (>200K) | $10.00 | 5 RPM / 50 RPD / 250K TPM — severely limited | Advanced reasoning. GPP T4 model. |
| Gemma 4 | `gemma-4` | varies | Free | Free | Yes | Open-weight. Free via Gemini API + self-hosting. |
| ~~Gemini 2.0 Flash~~ | ~~`gemini-2.0-flash-001`~~ | N/A | N/A | N/A | N/A | **DISCONTINUED June 1, 2026. DO NOT USE.** |
| ~~Gemini 2.0 Flash-Lite~~ | ~~`gemini-2.0-flash-lite-001`~~ | N/A | N/A | N/A | N/A | **DISCONTINUED June 1, 2026. DO NOT USE.** |

**URGENT:** Any code or config referencing `gemini-2.0-flash` or `gemini-2.0-flash-lite` must be migrated immediately to `gemini-3.1-flash-lite`.

---

### Free Tier — Complete Guide

**How to access:**
1. Go to https://aistudio.google.com — sign in with any Google account
2. Create an API key (tied to a GCP project — Google creates one automatically)
3. No credit card required. No billing needed. Key works immediately.
4. API endpoint: `https://generativelanguage.googleapis.com/v1beta/`

**Free tier rate limits (per GCP project, verified June 2026):**

| Model | RPM | RPD | TPM |
|---|---|---|---|
| `gemini-3.1-flash-lite` | 15 | 1,500 | 250,000 |
| `gemini-3-flash-preview` | 10 | 1,500 | 250,000 |
| `gemini-2.5-flash` | 10-15 | 250-1,500 | 250,000 |
| `gemini-2.5-flash-lite` | 15-30 | 1,000-1,500 | 250,000 |
| `gemini-2.5-pro` | 5 | 50 | 250,000 |

Note: Google reduced free tier quotas by 50-80% in December 2025 (fraud/abuse crackdown). Exact limits vary by project and region. Check live limits at: https://aistudio.google.com/rate-limit

**What triggers billing:**
- Free tier = NO billing account linked to the GCP project
- The moment you link a Cloud Billing account, ALL usage on that project becomes billable from token 1 — the free allowance disappears entirely for that project
- Billing is per-project: project without billing = always free (up to rate limits)

---

### Multi-Account Free Pool Strategy

Rate limits are per GCP PROJECT, not per API key. Multiple API keys under the same project share one quota pool. **Separate GCP projects each get their own independent free quota.**

**Setup for 8 Google accounts:**
- Create 1 GCP project per Google account (the auto-created project from AI Studio is sufficient)
- Generate 1 API key per project
- Result: 8 keys, each with its own quota bucket
- For `gemini-3.1-flash-lite`: 8 × 1,500 RPD = **12,000 requests/day free**
- For `gemini-2.5-flash`: 8 × 250-1,500 RPD = 2,000-12,000 requests/day free
- Rotate keys in the proxy round-robin; when one hits rate limit, advance to next
- Daily reset: midnight Pacific time

**CamClaw GFP pool status:** 28 keys across 8 Google accounts / 560 RPD already operational on `localhost:3761`.

**ToS note:** Using genuine accounts (not accounts created solely to bypass limits) is legitimate.

**IMPORTANT: Billing on one project does NOT affect other projects.** Keep free projects strictly separate from paid projects.

---

### GCP Billing Safety Controls

**Disable billing on free projects:**
- GCP Console > Billing > My Projects > [project] > Change Billing > Disable
- Or: via `gcloud billing projects unlink PROJECT_ID`
- Once unlinked, project returns to free tier immediately

**Budget alerts (alerts only — do NOT stop spending by default):**
- GCP Console > Billing > Budgets & Alerts > Create Budget
- Set thresholds at $10, $20, $50

**Hard spend cap (requires automation):**
GCP budget alerts are NOTIFICATIONS ONLY by default. To create a hard cap, set up a Cloud Function triggered by Pub/Sub budget notification that calls `billing.disableBillingForProject()`:
```
Billing > Budgets & Alerts > Create Budget
→ Set alerting pub/sub topic
→ Deploy Cloud Function triggered by that topic
→ Function calls Cloud Billing API to disable billing at threshold
```
Reference: https://docs.cloud.google.com/billing/docs/how-to/disable-billing-with-notifications

Recommended: set hard cap at $25 on `openclaw-io` project.

---

### Paid Tier (GPP — Gemini Pro Pool)

**Project:** `openclaw-io` (billing enabled)
**Model:** `gemini-2.5-pro` — $1.25/M input (≤200K context) / $2.50/M input (>200K) / $10.00/M output
**Context:** 1M tokens via Developer API (2M tokens on Vertex AI)
**Context caching:** 90% discount on cached tokens — significant savings for repeated long prompts
**Best for:** Extended thinking, SPORT/Docs/Wiki content generation, deep document analysis, research chains (OC T4 role)

**Key vault variables:**
- `GEMINI_FREE_KEY_1` through `GEMINI_FREE_KEY_8` — GFP pool (free projects)
- `GEMINI_PRO_KEY` or `POOL_KEY` — GPP paid tier (openclaw-io project)
- `GOOGLE_GENERATIVE_AI_API_KEY` — standard OC/SDK env var name

---

### OpenCode Gemini Provider Configuration

```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "google": {
      "options": {
        "apiKey": "{env:GOOGLE_GENERATIVE_AI_API_KEY}"
      },
      "models": {
        "gemini-2.5-flash": {},
        "gemini-2.5-flash-lite": {},
        "gemini-3-flash-preview": {},
        "gemini-3.1-flash-lite": {},
        "gemini-2.5-pro": {
          "options": {
            "thinkingConfig": { "thinkingBudget": 8192, "includeThoughts": true }
          }
        },
        "gemini-3-flash-preview": {
          "options": {
            "thinkingConfig": { "thinkingLevel": "high", "includeThoughts": true }
          }
        }
      }
    }
  },
  "model": "google/gemini-3.1-flash-lite"
}
```

**Available Gemini model IDs in OpenCode (confirmed June 2026):** Run `opencode models google` to get live list.
- `gemini-2.5-flash`
- `gemini-2.5-flash-lite`
- `gemini-2.5-pro`
- `gemini-3-flash-preview`
- `gemini-3.1-flash-lite`
- `gemini-3.1-pro-preview` (paid only)

---

### Antigravity — What It Actually Is

**IMPORTANT CLARIFICATION:** Antigravity is NOT an Anthropic product. It is a **Google DeepMind product** — an agent-first VS Code fork launched November 2025.

**What it is:**
- A full VS Code fork with integrated Agent Manager and browser
- Default model: Gemini 3 Flash (Google's, free during public preview)
- Also supports: Claude Sonnet 4.5, GPT-OSS
- Claude Code integrates as an **extension** inside Antigravity — not native
- Claude Code in Antigravity still requires its own CC authentication (Pro/Max subscription or API key)

**Models available in Antigravity (June 2026):**
- Gemini 3.1 Pro (High thinking) — weekly quota, resets every 7 days
- Gemini 3.1 Pro (Low thinking) — faster, shorter latency
- Gemini 3 Flash — fastest option, 15-30s typical response
- Claude Sonnet 4.5 (via Anthropic partnership)
- Claude Opus 4.5 (via Anthropic partnership)
- Claude Sonnet 4.5 Thinking
- Claude Opus 4.5 Thinking

**OpenCode Antigravity plugin (safer than CC proxy):**
```
npm install -g opencode-antigravity-auth
```
Provides dual quota: Antigravity quota + Gemini CLI quota. Auto-rotates between multiple authenticated Google accounts. Config in `~/.config/opencode/opencode.json`:
```json
{
  "plugin": ["opencode-antigravity-auth@latest"],
  "provider": {
    "google": {
      "models": {
        "antigravity-gemini-3-pro": { "options": { "thinkingConfig": { "thinkingLevel": "low" } } },
        "antigravity-gemini-3.1-pro": { "options": { "thinkingConfig": { "thinkingLevel": "high" } } },
        "antigravity-gemini-3-flash": { "options": { "thinkingConfig": { "thinkingLevel": "minimal" } } }
      }
    }
  }
}
```

**Antigravity proxy warning:** Community tools (badrisnarayanan/antigravity-claude-proxy, SovranAMR/claude-code-via-antigravity) proxy Antigravity's free Claude/Gemini quota to Claude Code CLI. Google has been issuing ToS violation bans (including shadow-bans and full account bans) for this usage pattern. **Avoid for CC.** The opencode-antigravity-auth plugin is safer (designed for OC, not CC bypass).

**Rate limits in Antigravity:** Weekly quota (not daily) that resets every 7 days. Exact numbers not published; check Settings > Models in Antigravity app. Google One subscribers get higher limits.

**Recommended Antigravity workflow (legitimate):** Use Gemini (free via Antigravity) for planning; Claude Code (paid) for implementation. Saves CC tokens during planning.

---

### Cascade UI: Paid Model Safety Controls (Gemini)

- All paid Gemini models **OFF by default** for new users
- Toggle: "Enable paid Gemini models (uses openclaw-io billing account)"
- Cost warning before enabling: "This will incur charges on your Google Cloud billing account. Estimated cost for gemini-2.5-pro: $1.25/M input tokens, $10.00/M output tokens."
- Wizard to check/disable GCP billing entirely for users wanting free-only: link to GCP Console billing unlink flow
- Budget alert integration: link to GCP Console > Billing > Budgets after enabling paid tier
- Ability to add all 8 Google accounts for free quota pool rotation

---

## 4. "Go" Models / OpenCode Other Providers

### OpenCode Go — Go Plan Models in Detail

**Plan:** $5 first month, $10/month. API endpoint: `https://opencode.ai/zen/go/v1/` (OpenAI-compatible). Can be used with CC or any OpenAI-compatible agent.

| Model | OC ID | Context | Req/5hr (Go) | Concurrent | Tier |
|---|---|---|---|---|---|
| DeepSeek V4 Flash | `opencode-go/deepseek-v4-flash` | 128K | ~31,650 | 20 | T3-free |
| DeepSeek V4 Pro | `opencode-go/deepseek-v4-pro` | 128K | ~3,300 | 3 | T2-exec |
| Kimi K2.6 | `opencode-go/kimi-k2.6` | 128K | 880-1,290 | 2 | T2-plan |
| GLM-5.1 | `opencode-go/glm-5.1` | 128K+ | ~4,300/month ceiling | 1 | T1 (OC) |
| Qwen 3.6 Plus | `opencode-go/qwen3.6-plus` | 1M | 3,300-3,450 | 5 | T2-plan fallback |
| MiniMax M2.7 | `opencode-go/minimax-m2.7` | 128K | included | varies | T2-exec fallback |

**OpenCode Go usage limits:** $12/5hr window, $30/week, $60/month. Free models available in Zen: `opencode/big-pickle`, `opencode/deepseek-v4-flash-free`, `opencode/nemotron-3-super-free` (~200K context, $0).

**Recommended Oh-My-OpenAgent task routing (OC):**
- quick / T3: V4 Flash (31,650 req/5hr, effectively free within Go)
- deep / complex T2: K2.6 → V4 Pro fallback
- writing / T2-plan: Qwen 3.6 Plus → V4 Pro fallback
- ultrabrain / T1: GLM-5.1 → K2.6 fallback

---

### Groq

**What it is:** LLM inference platform using custom LPU (Language Processing Unit) chips. Provides 394-1,000+ tokens/second depending on model. Available as a provider in OpenCode.

**How to access:** console.groq.com — free account, no credit card for basic tier.

**Free tier:** 30 RPM / 6,000 TPM / 1,000 RPD per model. RPD is the binding constraint.
**Developer tier:** Add credit card (still free). 10x rate limits + 25% token discount.
**Batch API + prompt caching:** 50% each, stackable to ~25% of on-demand pricing.

**Available free models:**

| Model | Groq ID | Context | Speed | Paid Input ($/MTok) | Best for |
|---|---|---|---|---|---|
| Llama 3.1 8B Instant | `groq/llama-3.1-8b-instant` | 131,072 | 560 TPS | $0.05 | T3-free. Fast classification, triage. |
| Llama 3.3 70B Versatile | `groq/llama-3.3-70b-versatile` | 131,072 | 394 TPS | $0.59 in / $0.79 out | T3 / light T2. Quality triage, summarization. |
| DeepSeek R1 Distill 70B | `groq/deepseek-r1-distill-llama-70b` | 131,072 | varies | varies | T2 reasoning. Architecture analysis free. |
| Llama 4 Scout 17B | `groq/llama-4-scout-17b` | 131,072 | fast | low | T3 general |
| Llama 4 Maverick 17B | `groq/llama-4-maverick-17b` | 131,072 | fast | low | T3/T2 general |
| Gemma 2 9B | `groq/gemma-2-9b-it` | 8,192 | fast | $0.20/$0.20 | T3 structured output |
| Qwen QwQ 32B | `groq/qwen-qwq-32b` | 131,072 | varies | varies | T2 reasoning |

**Groq OpenCode provider config:**
```json
{
  "provider": {
    "groq": {
      "models": {
        "llama-3.1-8b-instant": { "name": "Llama 3.1 8B (fast triage)" },
        "llama-3.3-70b-versatile": { "name": "Llama 3.3 70B (quality triage)" },
        "deepseek-r1-distill-llama-70b": { "name": "DeepSeek R1 70B (reasoning)" }
      }
    }
  }
}
```

**Auth:** `export GROQ_API_KEY=gsk_xxxx` or via `/connect` in OpenCode.

---

### Local LLMs (Ollama)

**What it is:** Open-source local LLM runner. CamClaw already runs Ollama at `172.18.0.1:11434` with `llama3.2:3b`.

**CRITICAL: Default context = 4096 tokens.** Must manually set `num_ctx` for coding use:
```
# Ollama Modelfile — Fix 4K Context Limit
FROM qwen2.5-coder:7b
PARAMETER num_ctx 32768
PARAMETER num_predict 4096

# Then: ollama create qwen2.5-coder-32k -f Modelfile
```

**Hardware requirements and recommended models:**

| RAM | Recommended model | Quality |
|---|---|---|
| 8GB | llama3.2:3b (current CamClaw), qwen2.5-coder:3b | Fast, low quality |
| 16GB | qwen2.5-coder:7b, codellama:7b | Good T3 |
| 32GB | gemma4:26b, qwen2.5-coder:14b | Strong T2 |
| 64GB+ | qwen2.5-coder:32b, llama3.3:70b | Near-API quality |

**Model must support tool use** for agentic coding. Verify before deploying.

**OpenCode Ollama provider config (pointing to CamClaw):**
```json
{
  "provider": {
    "ollama": {
      "baseURL": "http://172.18.0.1:11434/v1",
      "models": {
        "qwen2.5-coder:7b": { "name": "Qwen2.5 Coder 7B (CamClaw)" },
        "llama3.2:3b": { "name": "Llama 3.2 3B (CamClaw fast)" }
      }
    }
  }
}
```

**Benefits:** Zero cost (compute only), zero data exfiltration — ideal for sensitive codebase work. Upgrade CamClaw CX33 from `llama3.2:3b` to `qwen2.5-coder:7b` for better coding quality within the 8GB RAM constraint.

---

### DeepSeek (OpenCode Go)

**DeepSeek V4 Flash:**
- ID: `opencode-go/deepseek-v4-flash`
- Context: 128K
- Go plan: ~31,650 requests/5hr window — effectively unlimited for T3 use
- 20 concurrent requests
- Best for: high-volume triage, classification, code completion, quick fixes, PR reviews

**DeepSeek V4 Pro:**
- ID: `opencode-go/deepseek-v4-pro`
- Context: 128K
- Go plan: ~3,300 requests/5hr window
- 3 concurrent requests
- Best for: bulk ticket execution, 3-5 file features, multi-step debugging, terminal automation
- LiveCodeBench benchmark: 93.5%

---

### Kimi K2.6 (OpenCode Go)

- ID: `opencode-go/kimi-k2.6`
- Context: 128K
- Go plan: 880-1,290 requests/5hr window (tightest quota among Go models)
- 2 concurrent requests max
- Best for: code review, sprint planning, multi-file refactoring (10+ files), long autonomous runs
- SWE-Bench Pro: 57-59%
- Use for T2-plan (CR passes, complex agentic tasks). Reserve; don't use for T3-shaped work.

---

### Generic / Background Usage Model Strategy

**Truly free (no cost, no credit card needed):**
1. **Groq Free Tier:** 1,000 RPD per model. Best: Llama 3.1 8B (fast), DeepSeek R1 Distill 70B (reasoning).
2. **Ollama Local:** Zero cost once hardware exists. CamClaw CX33 already running.
3. **Google AI Studio / GFP Pool:** `gemini-3.1-flash-lite`, 28 keys, 560 RPD. CamClaw proxy at `localhost:3761`.
4. **Anthropic Haiku (CC Max plan):** Included in $200/mo, essentially unlimited vs usage.

**Near-free flat rate (no per-token cost anxiety):**
1. **OpenCode Go ($10/mo):** V4 Flash within Go = effectively unlimited T3. $60/month ceiling.
2. **Claude Max $200/mo:** Sonnet 4.6 quota runs 10-30% weekly.

**Design principle:**
- Generic/background tasks (classification, triage, label assignment, status checks, ack messages): run automatically via free-pool models with no user cost concern
- Specialized/premium tasks (architecture decisions, complex coding, adversarial review): require explicit user opt-in and justification

**Fallback chain (automatic, no user action):**
```
Local Ollama → Gemini GFP (free pool) → Groq free tier → (ask user) → paid tier
```

---

## 5. Cascade App — Setup Wizard & UX Requirements

### Subscription Detection & Configuration

**Auto-detect on app launch (scan in order):**

| Signal | Detected as |
|---|---|
| `~/.claude/` directory exists + `settings.json` with model | CC harness (Claude.app) |
| `~/.claude-acc1/`, `~/.claude-acc2/` etc. | CC multi-account setup |
| `~/.config/opencode/opencode.json` exists | OC harness |
| `opencode` binary in PATH | OC available |
| `ollama` binary in PATH or `172.18.0.1:11434` responds | Local Ollama available |
| `GOOGLE_GENERATIVE_AI_API_KEY` in env or vault.env | Gemini API configured |
| `OPENAI_API_KEY` in env or vault.env | OpenAI API configured |
| Antigravity app installed at `/Applications/Antigravity.app` | Antigravity available |

**vault.env scan:** `~/.claude/vault.env` is the canonical credential source. Cascade should source it (read-only) during setup to detect which providers are already configured.

---

### Paid Model Safety Controls (CRITICAL for Cascade UI)

**Master switch:** "Disable all paid models" — overrides all per-provider toggles. Visible in the main settings panel, not buried.

**Per-provider enable/disable toggles (all OFF by default for new users):**

| Provider | Default | Toggle label | Cost warning text |
|---|---|---|---|
| Gemini paid models | OFF | "Enable paid Gemini models" | "Charges to your Google Cloud billing account (openclaw-io). gemini-2.5-pro: ~$1.25/M input tokens." |
| OpenAI API | OFF | "Enable OpenAI API" | "Charges to your OpenAI API account. GPT-5.5: $5/M input tokens." |
| Anthropic API (direct) | OFF | "Enable Anthropic API (direct)" | "Claude Max plan is recommended instead. Direct API: $3/M input tokens for Sonnet." |
| Groq paid | OFF | "Enable Groq paid tier" | "Groq free tier (1,000 RPD) is available. Paid tier provides 10x limits." |

**Free models — always ON (no toggle required):**
- Gemini GFP pool (free projects, billing disabled)
- Groq free tier (no credit card)
- Local Ollama
- CC Max plan models (Claude.app / Claude2.app — subscription pre-paid)
- OC Go plan models (OpenCode Go subscription — subscription pre-paid)

**Gemini billing wizard (triggered when user tries to enable paid Gemini):**
1. "Check current GCP billing status for your projects"
2. Link to https://console.cloud.google.com/billing for user to verify
3. Option: "I want free-only Gemini" → wizard guides user to unlink billing from all their GCP projects
4. Option: "I want paid Gemini (openclaw-io)" → show $25 hard cap setup instructions
5. "Set budget alert" button → link to GCP Console > Billing > Budgets
6. Confirmation: "Paid models will only activate for projects with billing enabled. Your free pool projects (billing disabled) remain free."

---

### Harness Setup Details

**CC Harness:**
- Detect `~/.claude/settings.json` and current model
- Two-app operation: Claude.app (Sonnet T0, personal ops) vs Claude2.app (Opus T0, coding/PEWS)
- Multi-account: detect `~/.claude-acc1/` through `~/.claude-acc4/` or equivalent
- Show per-account quota via `/aa` integration
- CC quota hit warning: "When CC session quota is hit, ALL models (including Haiku) become unavailable until reset. There is no T3 fallback."

**OC Harness:**
- Detect `~/.config/opencode/opencode.json`
- Show current provider configuration
- Offer to configure Go plan if `opencode-go` provider not present
- OC quota hit: "Primary models unavailable. T3 fallback to DeepSeek V4 Flash continues XS/S tickets. OC is more resilient than CC on quota hits."
- Show OC Go budget: $12/5hr, $30/week, $60/month remaining

**Gemini (8-account free pool):**
- Add Google accounts: "Add Google account" button → auth flow → creates/detects GCP project → generates API key
- Show per-account quota status (RPM/RPD remaining)
- Pool visualization: 8 circles, each representing one account's quota bucket
- Key storage: save to vault.env as `GEMINI_FREE_KEY_1` through `GEMINI_FREE_KEY_8`
- Proxy status: show `localhost:3761` connectivity and round-robin rotation status

**Local Ollama:**
- Detect running Ollama at `localhost:11434` and `172.18.0.1:11434` (CamClaw)
- List available models via `ollama list`
- Show installed model context windows (flag if < 8K — likely 4K default)
- Offer to create extended-context Modelfile (qwen2.5-coder-32k)
- Hardware detection: show available RAM → recommend appropriate model tier
- Zero-cost badge: "All inference is local. No tokens leave your machine."

---

### Setup Wizard Flow

**Step 1: Welcome + Subscription Detection**
- Scan for existing configuration (CC, OC, vault.env, Ollama)
- Show "What we found" summary card
- Estimated monthly cost based on detected subscriptions

**Step 2: CC Harness**
- Confirm Claude.app scope: "Personal operations, email, research, CamClaw" (Sonnet T0)
- Confirm Claude2.app scope: "Coding projects, PEWS Build/Plan" (Opus T0)
- If 4 accounts detected: show priority chain, confirm rotation order
- Test: send a test prompt via CC, confirm response
- Show quota: "Sonnet: X% used this week | Opus: Y% used this week"

**Step 3: OC Harness**
- Detect OpenCode binary and config
- Configure OpenCode Go provider (if subscription exists)
- Configure OpenAI provider (prompt for API key if wanted, with cost warning)
- Show model tier table for OC
- Test: send a test prompt via OC

**Step 4: Gemini Free Pool**
- "You have [N] Google accounts. Each can provide free Gemini quota."
- Walk through adding each account
- Verify each project has billing DISABLED (show GCP billing status)
- Configure round-robin proxy if not already running
- PAID tier opt-in (separate, with full cost warning flow)

**Step 5: Local Models**
- Detect Ollama
- List installed models + context windows
- Recommend model upgrade if hardware supports it
- Context window fix offer (Modelfile creation)
- Test: run a local prompt, show latency

**Step 6: Generic Usage Configuration**
- "Which models should Cascade use for automatic background tasks?"
- Default selection (pre-checked, all free):
  - [x] Gemini GFP pool (free, 560 RPD total)
  - [x] Local Ollama — llama3.2:3b (zero cost)
  - [ ] Groq free tier (opt-in, requires account)
  - [ ] Paid API models (disabled by default)
- Show fallback chain visualization: Local → GFP → Groq → (pause, ask user) → Paid

**Step 7: Test Connections**
- Ping each configured endpoint
- Green/red status per provider
- Show estimated capacity: "Your free tier supports approximately X requests/day"

**Step 8: Summary**
- Active harnesses: CC (4 accounts), OC (Go plan), Gemini (8-account pool), Ollama
- Paused (paid, off): OpenAI API, Gemini paid, Groq paid
- Monthly cost: $800/mo (4x Claude Max) + $40/mo (4x OC Go) + $0 (Gemini free) + $0 (Ollama)
- "Paid models are disabled. Enable them in Settings > Providers anytime."

---

### In-App Settings (Post-Wizard)

**Providers panel:**
- Per-provider toggle (enable/disable)
- Per-provider status: quota remaining, last used, cost this month
- "Edit credentials" for each provider
- "Test connection" button per provider

**Cost monitoring:**
- Session cost tracker (per-provider breakdown)
- Weekly cost summary
- Month-to-date per provider
- Alert threshold: "Warn me when session cost exceeds $X"

**Account rotation:**
- CC account priority order (drag to reorder)
- Gemini pool rotation strategy (round-robin vs least-used)
- Manual override: "Force route to account X for this session"

**Master controls:**
- "Disable all paid models" — red button, prominent placement
- "Free-only mode" — enables only free-tier and flat-rate subscription models
- "Emergency stop" — pauses all API calls immediately

---

### Full Multi-Provider opencode.json Reference

```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "opencode-go/deepseek-v4-flash",
  "provider": {
    "opencode-go": {},
    "opencode": {},
    "groq": {},
    "google": {
      "options": {
        "apiKey": "{env:GOOGLE_GENERATIVE_AI_API_KEY}"
      },
      "models": {
        "gemini-3.1-flash-lite": {},
        "gemini-2.5-flash": {},
        "gemini-2.5-pro": {
          "options": {
            "thinkingConfig": { "thinkingBudget": 8192, "includeThoughts": true }
          }
        }
      }
    },
    "ollama": {
      "baseURL": "http://172.18.0.1:11434/v1",
      "models": {
        "qwen2.5-coder:7b": { "name": "Qwen2.5 Coder 7B (CamClaw)" },
        "llama3.2:3b": { "name": "Llama 3.2 3B (CamClaw fast)" }
      }
    },
    "anthropic": {},
    "openai": {}
  },
  "agent": {
    "coder":     { "model": "opencode-go/deepseek-v4-pro" },
    "reviewer":  { "model": "opencode-go/kimi-k2.6" },
    "architect": { "model": "opencode-go/glm-5.1" },
    "explore":   { "model": "opencode-go/deepseek-v4-flash" },
    "triage":    { "model": "opencode-go/deepseek-v4-flash" },
    "t1-openai": { "model": "openai/gpt-5.5" },
    "t2-codex":  { "model": "openai/gpt-5.3-codex" },
    "t3-groq":   { "model": "groq/llama-3.1-8b-instant" },
    "t3-local":  { "model": "ollama/llama3.2:3b" }
  }
}
```

---

### Quick Reference: Model IDs by Harness and Tier

**CC (Claude Code):**
```
T1: claude-opus-4-8          (scarce, surgical, written justification required)
T2: claude-sonnet-4-6        (abundant, default workhorse)
T3: claude-haiku-4-5         (effectively free, high-volume triage)
```

**OC (OpenCode) — Go plan:**
```
T0: google/gemini-3.1-flash-lite (GFP pool, 28 keys)
T1: openai/gpt-5.5
T2-plan: opencode-go/kimi-k2.6
T2-exec: opencode-go/deepseek-v4-pro
T2-code: openai/gpt-5.3-codex
T3: opencode-go/deepseek-v4-flash  (or google/gemini-3.1-flash-lite)
T3-free: opencode-go/deepseek-v4-flash  (quota-hit fallback)
T4: google/gemini-2.5-pro  (GPP paid, extended thinking)
```

**Groq (supplemental T3-free for OC):**
```
T3: groq/llama-3.1-8b-instant        (fastest, 560 TPS, 1K RPD free)
T3: groq/llama-3.3-70b-versatile     (quality, 394 TPS, 1K RPD free)
T2-reasoning: groq/deepseek-r1-distill-llama-70b  (free, reasoning capable)
```

**Gemini (free pool via 8 accounts):**
```
GFP T3: gemini-3.1-flash-lite   (15 RPM / 1,500 RPD per project = 12K RPD total across 8 accounts)
GPP T4: gemini-2.5-pro          (paid, openclaw-io only, 1M context)
DEPRECATED: gemini-2.0-flash    (SHUT DOWN June 1, 2026 — migrate immediately)
```

**Local (CamClaw Ollama at 172.18.0.1:11434):**
```
Current: ollama/llama3.2:3b     (fast, low quality — good for local smoke tests)
Upgrade: ollama/qwen2.5-coder:7b  (better coding quality, fits 8GB RAM)
Context: MUST set num_ctx 32768 in Modelfile (default = 4096)
```

---

### Mellum2 (JetBrains) — evaluated 2026-06-02 → integrate-not-bundle, hardware-gated optional

JetBrains open-sourced **Mellum2** (12B MoE / 2.5B active / 64 experts, Apache 2.0) on 2026-06-01 as a "focal model" for the infra layer of agentic systems. Evaluated as a Cascade local-model candidate via adversarial-verified deep research. Verdict: **optional provider entry, NOT default, NOT bundled.**

**Why it's a genuine fit (not niche):** JetBrains explicitly names routing, RAG pipeline processing, context compression, and summarization as *primary* targets — the exact jobs Cascade orchestrates. Mellum2-Thinking beats Qwen3.5-9B on LiveCodeBench (69.9% vs 68.3%) and is within ~6pts on MMLU-Redux (86.2% vs 91.7%). It is stronger than JetBrains' own modest "focal model" framing implies.

**Why it can't be the default or bundled (verified blockers, 2026-06-02):**
- No Ollama library entry — `ollama pull mellum2` does not exist (only the unrelated Mellum 4B is listed).
- No official GGUF — JetBrains shipped safetensors/BF16 only; one ~5hr-old community GGUF (Thinking variant, user RJ000).
- 12B-MoE = all weights resident (~6.6GB @ Q4_K_M) even though only 2.5B activate → **dead on 8GB CamClaw**, tight on 16GB Mac (short-context only), comfortable at ≥24GB RAM / ≥16GB VRAM.
- All throughput claims (21% faster than Qwen2.5-7B; sub-second inference) are JetBrains-self-reported on H100/vLLM — zero third-party reproduction as of release.
- Bundling ~7-8GB weights has zero FOSS precedent (Continue.dev / Aider / Twinny / Cody all ship zero weights, delegate to Ollama) and would couple Cascade's release cycle to the model's.

**Cascade action:**
1. Optional provider entry under "Local / Advanced," gated to show only if RAM ≥ 24GB or VRAM ≥ 16GB. Label: "Mellum2 12B (JetBrains, code-specialized, MoE)".
2. Link to HuggingFace for manual GGUF import — do NOT ship or auto-download weights.
3. Tracking ticket: auto-enable `ollama pull mellum2` once the official Ollama library entry lands.
4. Keep `qwen2.5-coder:7b` as the default local model; `llama3.2:3b` for the 8GB floor.
5. Revisit in 4-6 weeks once official quantizations + independent benchmarks exist.

Legitimate Cascade slots once deployable: query-routing decisions, context-compression passes, code-edit sub-agent work, summarization. Not a fit: multimodal, frontier reasoning, knowledge-heavy queries. (RAG *reranking* stays on BGE-M3 + cross-encoder — that is not generative-LLM work.)
