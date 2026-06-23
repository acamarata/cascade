# Model Access & Routing — Research Reference

*Source of truth for how Cascade selects and dispatches models. Feeds the
accounts subsystem, `cascade dispatch`, and the quota-aware router. See also
`data/model-matrix.json` (machine-readable) and the wiki page
`Accounts-and-Models.md` (user-facing).*

---

## Account families

### Claude (Anthropic) — multiple accounts

- **Access**: native Claude Code (`claude`) for the primary account; extra
  accounts (`acc2`, `acc3`, …) via `claude -p` run inside a **PTY** (smithers /
  claude-p). PTY is required because Claude Code's interactive mode does not
  expose a headless exec interface natively; the PTY wrapper drives it.
- **Subscription tiers**: Anthropic Pro / Max. Cascade reads the per-window
  usage from `quota-store.json`; overage (pay-per-token) is **off by default**
  and requires explicit opt-in.
- **Models available**: Fable-5, Opus-4.8, Sonnet-4.6, Haiku-4.5.
- **Drain order**: acc2+ exhausted first; the primary account (acc1) is reserved
  for interactive chat. Extra accounts are detected from `accounts.json` and
  dispatched round-robin within available quota.

### Codex (OpenAI) — multiple accounts

- **Access**: `codex exec -p <prompt>` (headless, no PTY required).
- **Subscription tiers**: OpenAI Pro / Max.
- **Models available**: GPT-5.5 (current Codex default).
- **CLI detection**: `which codex`. If absent, Cascade skips this lane; it never
  fabricates a result.

### Gemini AGY (Google AI Pro/Ultra) — multiple accounts

- **Access**: `agy -p <prompt>` (headless).
- **Subscription tiers**: Google AI Pro / Ultra.
- **Models available**: Gemini-3.1-Pro.
- **CLI detection**: `which agy`.

### OpenCode-Go + Zen — open models

- **Access**: `opencode run <prompt>` (headless, dollar-metered).
- **Models available**: GLM-5.2 and other open / hosted models configured in
  OpenCode's provider list.
- **Cost**: pay-per-token against the configured provider; not free.
- **CLI detection**: `which opencode`.

### GFP — Gemini Flash Free Pool

- **Access**: a pool of API keys (multiple Google accounts / GCP projects) stored
  in `accounts.json`, used round-robin. No CLI required — Cascade calls the
  Gemini REST API directly.
- **Models available**: Gemini-3.5-Flash (free tier, rate-limited per key).
- **Pool size**: ~27 keys in the reference deployment; the actual count scales
  with how many accounts the user has added. Keys rotate on quota exhaustion.
- **Cost**: free at the model tier Cascade uses (Flash free quota). No spend
  unless the user explicitly upgrades a key to a paid tier.
- **Private**: key values stay in `~/.cascade/accounts.json` and never leave the
  machine. The count and round-robin state live in `quota-store.json`.

---

## Models — best-for matrix

| Model | Family | Access method | Best for | Avoid for |
|---|---|---|---|---|
| **Claude Fable-5** | Anthropic | Claude Code PTY (acc2+) | Hardest multi-step reasoning, architecture decisions, final correctness gates, code synthesis where subtlety matters | Cheap/bulk work — burns subscription quota fast |
| **Claude Opus-4.8** | Anthropic | Claude Code PTY (acc2+) | Architecture review, adversarial CR-C, final gate when Fable is saturated | Same as Fable — surgical use only |
| **Claude Sonnet-4.6** | Anthropic | Claude Code native (acc1 T0) / PTY (acc2+) | Interactive chat (acc1), bulk execution, code-gen, docs drafting, T2 agents | T0 acc1 is reserved for user chat; don't burn it on background tasks |
| **Claude Haiku-4.5** | Anthropic | Claude Code PTY (acc2+) | Fast triage, lightweight summaries, mechanical sweeps, cheap agents | Complex reasoning or output that requires nuance |
| **GPT-5.5** | OpenAI | `codex exec` | Cross-family CR/QA (independent second opinion), adversarial review, strong general reasoning | Routing decision authority — use as peer, not oracle |
| **Gemini-3.1-Pro** | Google | `agy -p` | Large-context research, documentation, cross-family CR/QA, strong at structured output | Sensitive personal data — external model, firewall applies |
| **GLM-5.2** (OpenCode-Go) | Open | `opencode run` | Bulk drafting, docs generation, open-model tasks where cost matters | High-stakes decisions; weaker than frontier models |
| **Gemini-3.5-Flash (GFP)** | Google | Key pool, REST API | Background helpers, taxonomy classification, post-prompt processing, cheap research, dynamic-learning tagging — used liberally because free and fast even though weaker | Sensitive data (firewall), final-gate decisions, tasks needing deep reasoning |

---

## Routing strategy

### Core principle: drain cheap/extra first, protect acc1

The primary Claude account (acc1, the one Claude Code runs as) is the user's
interactive session. Every background task that consumes it shortens the window
available for real work. Cascade's dispatcher follows a strict order:

1. Classify the task against the matrix below.
2. Query `quota-store.json` for per-source headroom.
3. Pick the highest-preference source that has remaining quota.
4. If all preferred sources are exhausted, step down to the next tier.
5. Never dispatch to an external model if the task is sensitive-flagged (see
   firewall).

### Task-class routing table

| Task class | First choice | Fallback chain |
|---|---|---|
| T0 interactive chat / final synthesis | acc1 Claude Sonnet (reserved) | — (no fallback; user is waiting) |
| Hard reasoning / final correctness gate | acc2+ Claude Fable-5 or Opus-4.8 | acc1 Claude (only if all extra exhausted) |
| Bulk execution / code drafting / agents | acc2+ Claude Sonnet (drain first) | Codex GPT-5.5 → OpenCode-Go GLM |
| Adversarial CR / QA / research | Cross-family preferred: GPT-5.5 → Gemini-3.1-Pro → OpenCode-Go | acc2+ Claude Haiku (same-family, lower value for adversarial) |
| Large-context research / docs | Gemini-3.1-Pro (`agy`) → GPT-5.5 | OpenCode-Go → acc2+ Claude Sonnet |
| Cheap / classify / taxonomy / post-prompt | GFP Gemini-3.5-Flash (maximize free quota) | Local LLM |
| **Sensitive: PII / VA / health / custody / personal** | **Claude only (any acc) or local** | **Never external, never synced — hard firewall** |

### Exhaustion and quota-awareness

Before any dispatch, `cascade dispatch` reads `~/.cascade/quota-store.json` (written
by the `cascaded` daemon every ~60 seconds). Each source has a per-window usage
counter. The router picks the best source **with headroom** — if acc2 is at its
hourly window limit, it skips to acc3, then Codex, then Gemini, etc.

Paid-API overage is **off by default** for all sources. The user must explicitly
enable it per-source in `accounts.json` (`"overage": true`). Without it, a
saturated source is treated as unavailable, not as a billable fallback.

### Cross-family independence

For adversarial CR/QA, Cascade deliberately avoids the same model family that
authored the code. A Claude-written function reviewed only by Claude is weaker
than one reviewed by GPT-5.5 or Gemini. The router enforces this: if the authoring
source is known (stored in the task record), it picks a different family for the
review pass.

### PTY requirement (Claude only)

Claude Code's `claude -p` mode requires a PTY to function correctly. smithers /
claude-p are the PTY wrapper libraries that Cascade uses. No other model family
needs this — `codex exec`, `agy -p`, and `opencode run` all support true headless
invocation.

### Sensitive-data firewall

Any content flagged sensitive (personal name/dob/family, VA records, medical,
custody, financial) is routed exclusively to Claude accounts or a local model. The
classifier runs before dispatch and hard-blocks the task from reaching Codex,
Gemini (agy or GFP), OpenCode-Go, or any external endpoint. This applies even
when all Claude quotas are exhausted — in that case, the task waits rather than
routing out.

### GFP maximization

The Gemini Flash Free Pool is intentionally maxed for all cheap work. It is weaker
than frontier models but free, fast, and available 24/7 via round-robin key
rotation. Cascade treats it as the default for anything that doesn't require
reasoning depth: taxonomy tagging, dynamic-learning classification, post-prompt
normalization, background summarization, cheap research passes. This deliberately
offloads work from paid quotas.

---

## Configuration reference

### `~/.cascade/accounts.json`

Central account registry. Cascade reads this at startup and passes relevant
entries to the quota poller and dispatcher. Shape:

```json
{
  "accounts": [
    {
      "id": "claude-acc1",
      "family": "claude",
      "role": "primary",
      "access": "native"
    },
    {
      "id": "claude-acc2",
      "family": "claude",
      "role": "extra",
      "access": "pty",
      "overage": false
    },
    {
      "id": "codex-acc1",
      "family": "openai",
      "role": "extra",
      "access": "codex-exec",
      "overage": false
    },
    {
      "id": "agy-acc1",
      "family": "google",
      "role": "extra",
      "access": "agy-p",
      "overage": false
    },
    {
      "id": "opencode-acc1",
      "family": "open",
      "role": "extra",
      "access": "opencode-run",
      "overage": false
    },
    {
      "id": "gfp-pool",
      "family": "google-free",
      "role": "pool",
      "access": "api-pool",
      "overage": false,
      "keys": ["..."]
    }
  ]
}
```

Key fields:
- `role: "primary"` — reserved for T0 interactive chat, never dispatched to
  by background tasks.
- `role: "extra"` — available for background dispatch, drained before primary.
- `role: "pool"` — key-pool source (GFP); the keys array is the round-robin set.
- `overage: false` — paid-API overages blocked (default). Set `true` per-account
  to allow.

### `~/.cascade/quota-store.json`

Written by `cascaded` daemon every ~60 seconds. Per-source window counters. The
dispatcher reads this; it never writes it. Schema in `data/model-matrix.json`.

### `config.toml`

```toml
[dispatch]
sensitive_firewall = true     # never route sensitive content externally
cross_family_review = true    # prefer different family for CR/QA
gfp_maximize = true           # use GFP for all cheap/classify work
overage_default = false       # per-account overage off by default

[fleet]
enabled = true
interval_secs = 60
```

---

## Capability notes (accurate as of 2026-06-23)

- **Fable-5** is Anthropic's strongest reasoning model as of this writing.
  Scarce; used only for final gates and hardest architecture decisions.
- **Opus-4.8** is the previous top-tier model; still strong for arch review and
  CR-C. Use when Fable-5 quota is exhausted.
- **Sonnet-4.6** is the daily-driver model — strong, fast, and the default for
  most agent work.
- **GPT-5.5** is OpenAI's current Codex default. Strong cross-family reviewer;
  provides independent perspective from Claude.
- **Gemini-3.1-Pro** excels at large-context tasks and structured output. Good
  for documentation and research passes.
- **GLM-5.2** (via OpenCode-Go) is a capable open model but weaker than frontier
  models. Good for bulk drafting where cost matters.
- **Gemini-3.5-Flash (GFP)** is fast and free but noticeably weaker than the
  above. Do not use it for tasks requiring depth; maximize it for tasks where
  volume matters more than quality.
- CLI availability: Cascade detects each CLI at startup via `which`. A missing
  CLI means that lane is unavailable — Cascade skips it without error.
