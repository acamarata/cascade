# Accounts & Models

Cascade tracks every AI account you have — Claude, Codex, Gemini, and more — in one local registry. It monitors live usage, then routes background work to whatever has quota headroom, keeping your main Claude session free for actual conversation.

Everything stays on your machine. No server, no sync, no telemetry beyond what you opt into.

---

## What Cascade tracks

| Provider | Tool | Models |
|---|---|---|
| Anthropic (Claude) | `claude` / `claude -p` | Fable-5, Opus-4.8, Sonnet-4.6, Haiku-4.5 |
| OpenAI (Codex) | `codex exec` | GPT-5.5 |
| Google AI (AGY) | `agy -p` | Gemini-3.1-Pro |
| OpenCode-Go + Zen | `opencode run` | GLM-5.2 and other open models |
| GFP (Gemini Flash Free Pool) | API key pool | Gemini-3.5-Flash (free tier) |

If a CLI is not installed, Cascade skips that lane — it never fabricates output or errors out on missing tools.

---

## How model selection works

Before routing any task, Cascade:

1. Checks `~/.cascade/quota-store.json` (refreshed every ~60 seconds by the background daemon) to see which accounts have quota headroom.
2. Classifies the task — is it cheap background work, a bulk draft, an adversarial review, or something sensitive?
3. Picks the best available source for that task class.

| Task | Goes to |
|---|---|
| You typing in the chat window | Your primary Claude account (always reserved) |
| Bulk drafting / agents / background execution | Extra Claude accounts (acc2, acc3…) first — they drain before acc1 |
| Code review / QA | A *different* model family than who wrote the code (cross-family = more independent) |
| Classification, tagging, cheap research | GFP Gemini-3.5-Flash — free and fast, maximized for all grunt work |
| Architecture decisions / final gate | Claude Fable-5 or Opus-4.8 (the strongest models, used sparingly) |
| Anything with personal data (PII, health, VA, custody) | Claude only, or a local model — hard firewall, never sent externally |

### Why cross-family for review?

A Claude-written function reviewed only by Claude is a weaker check than one reviewed by GPT-5.5 or Gemini. Cascade enforces this deliberately: the reviewer is always a different model family than the author when possible.

### Why drain extra Claude accounts first?

Your primary Claude account is your interactive session. If background agents consume its quota, your chat slows down or stops. Cascade routes all background work to acc2, acc3, etc. first, and only falls back to acc1 when every extra account is exhausted.

---

## Best-for summary

| Model | Best for |
|---|---|
| **Fable-5** | Hardest reasoning, architecture, final correctness gate |
| **Opus-4.8** | Architecture review, adversarial CR-C; use when Fable quota is gone |
| **Sonnet-4.6** | Everyday work, bulk agents, code generation, docs |
| **Haiku-4.5** | Fast triage, lightweight sweeps, cheap T3 agents |
| **GPT-5.5** | Cross-family CR/QA, independent second opinion on Claude-authored code |
| **Gemini-3.1-Pro** | Large-context research, documentation, structured output |
| **GLM-5.2** | Bulk drafting on a budget; open-model tasks |
| **Gemini-3.5-Flash (GFP)** | Background helpers, taxonomy, classification, post-prompt cleanup — free, fast, use liberally |

---

## Adding accounts

```sh
cascade accounts add
```

The interactive wizard walks you through adding a new account. You will be asked:
- Which provider (Claude, Codex, Google, OpenCode-Go, GFP pool key)
- An account identifier (e.g. `claude-acc2`, `agy-work`)
- Whether to allow paid-API overage (default: off)

Accounts are stored in `~/.cascade/accounts.json`. Keys for GFP are stored there too — they never leave your machine.

To list what is registered:

```sh
cascade accounts list
```

To remove an account:

```sh
cascade accounts remove <id>
```

---

## The central registry: `~/.cascade/accounts.json`

Every account Cascade knows about lives here. A minimal example:

```json
{
  "accounts": [
    { "id": "claude-acc1", "family": "claude", "role": "primary" },
    { "id": "claude-acc2", "family": "claude", "role": "extra", "overage": false },
    { "id": "codex-acc1", "family": "openai",  "role": "extra", "overage": false },
    { "id": "agy-acc1",   "family": "google",  "role": "extra", "overage": false },
    { "id": "gfp-pool",   "family": "google-free", "role": "pool", "keys": ["..."] }
  ]
}
```

- `role: "primary"` — your main Claude session; background tasks never consume it.
- `role: "extra"` — drained first for background work.
- `role: "pool"` — a key pool (GFP); keys rotate round-robin.
- `overage: false` — paid-API overage is blocked by default. Set to `true` to allow a source to exceed its subscription limit at your cost.

---

## Menu-bar / widget readout

The Cascade menu-bar tray shows a live usage summary pulled from `quota-store.json`. Each row is one account or pool, with a usage bar and the time of the last successful poll.

Sources without a live reader show nothing — Cascade never fabricates usage numbers.

---

## GFP — the free Gemini Flash pool

The Gemini Flash Free Pool is a set of API keys across multiple Google accounts or GCP projects, used round-robin. Each key has its own free-tier quota. When one key hits its window limit, the next one takes over.

This pool is maximized deliberately: Cascade routes all cheap background work here first, saving your paid quotas for tasks that actually need them. The pool grows as you add more Google accounts via `cascade accounts add`.

Key values live only in `~/.cascade/accounts.json`. They are never synced, logged, or shared.

---

## Paid-API overage

Off by default for every account. If you want a source to continue working after its subscription quota runs out (billed per token), set `"overage": true` for that account in `accounts.json` or toggle it in the onboarding wizard.

Multi-account sub-pooling (sharing subscription capacity across accounts) is a private, opt-in, feature-gated capability. It is not advertised in the public release.

---

## Sensitive-data firewall

If Cascade detects that a task involves personal data — names, dates of birth, family relationships, VA or medical records, custody documents, financial information — it routes the task to a Claude account or a local model only. External providers (Codex, Gemini AGY, GFP, OpenCode-Go) are hard-blocked for sensitive content, even if all Claude quotas are exhausted. The task waits rather than routing out.

This is on by default and cannot be disabled without recompiling Cascade.

---

## Related pages

- [Fleet & Quota](Fleet-and-Quota.md) — how the quota poller works
- [Daemon Architecture](Daemon-Architecture.md) — cascaded internals
- [Privacy](Privacy.md) — what stays local and why
- [Configuration](Configuration.md) — full config.toml reference
