# Anthropic / Claude — Deep Detail

Verified 2026-07-06, live-fetched from platform.claude.com and support.claude.com (not training memory). See `models/README.md` for the master matrix; see `models/models.yaml` for the machine-readable entries.

Cascade accounts: **A1** (primary, `~/.claude`), **A2** (second Max seat, `~/.claude2` via `smithers-claude-p` PTY wrapper). Dispatch: native Claude Code.

---

## Claude Opus 4.8 — `claude-opus-4-8`

- **Status:** GA, current top Opus-tier model. [Certain]
- **Best for:** Complex agentic coding and enterprise work; long-horizon autonomous agentic execution, knowledge work, memory tasks. Anthropic's own guidance: "if unsure, start with Opus 4.8."
- **Context window:** 1M tokens (default). Max output: 128K tokens (300K on Batch API with `output-300k-2026-03-24` beta).
- **Thinking:** Extended thinking = No (removed); Adaptive thinking = Yes. `effort` defaults to `high` on all surfaces (API, Claude Code, claude.ai).
- **API price:** $5/MTok input, $25/MTok output. [Certain]
- **Max-plan quota weight:** Baseline reference point most other models/weightings are quoted against ("Xx of Opus"). Weekly limits: Max plans have **two weekly caps** — one all-models cap and one Sonnet-only cap; Opus has its own separately-tracked reset schedule for monitoring purposes but the only formally separate documented *cap* is the Sonnet one. Anthropic does **not publish exact numeric hour/token figures** — treat any blog quoting fixed "40 hours/week Opus" as stale/unverified. [UNVERIFIED: exact numeric weekly cap]

## Claude Sonnet 5 — `claude-sonnet-5`

- **Status:** GA, confirmed current. [Certain]
- **Best for:** "Best combination of speed and intelligence" — near-Opus quality on coding/agentic work at Sonnet cost and latency. Default recommended model for most day-to-day coding. This is Cascade's `DEFAULT_HARNESS_MODEL`.
- **Context window:** 1M tokens. Max output: 128K tokens (300K Batch API beta).
- **Thinking:** Extended thinking = No; Adaptive thinking = Yes (on by default when `thinking` omitted). `effort` supports full `low`/`medium`/`high`/`xhigh`/`max` range (first Sonnet-tier model with `xhigh`); defaults to `high` on API and Claude Code.
- **API price:** $3/MTok input, $15/MTok output — **introductory pricing $2/$10 per MTok through 2026-08-31**. [Certain]
- **Max-plan quota weight:** Has its **own dedicated weekly sub-cap** on Max plans (the "Sonnet-only" weekly limit, in addition to the all-models cap) — Sonnet usage is metered both against the shared pool and against a Sonnet-specific ceiling. Cheapest per-turn burn of the frontier-tier models. Official Claude Code guidance: "Opus costs several times more per turn than Sonnet, and Sonnet more than Haiku" (no exact multiplier published). [Certain: separate Sonnet weekly cap exists; UNVERIFIED: exact multiplier vs Opus/Haiku]

## Claude Haiku 4.5 — `claude-haiku-4-5` (dated: `claude-haiku-4-5-20251001`)

- **Status:** GA, current. [Certain]
- **Best for:** "Fastest model with near-frontier intelligence" — quick/high-volume tasks, subagent/triage work, cost-sensitive routing.
- **Context window:** 200K tokens. Max output: 64K tokens.
- **Thinking:** Extended thinking = Yes (only current model still on the legacy `budget_tokens` extended-thinking path); Adaptive thinking = No.
- **API price:** $1/MTok input, $5/MTok output. [Certain]
- **Max-plan quota weight:** Cheapest per-turn burn of all four ("fastest and cheapest option" per official Claude Code guidance). Counts against the same all-models weekly cap as everything except Sonnet's dedicated sub-cap; no Haiku-specific sub-cap exists. [Certain on relative cheapness; UNVERIFIED exact multiplier]

## Claude Fable 5 (Mythos-class) — `claude-fable-5` (org-gated twin: `claude-mythos-5`, Project Glasswing only)

- **Status:** GA as of 2026-06-09, briefly pulled and **redeployed 2026-07-01** with an improved safety classifier (blocks the reported jailbreak technique in "over 99% of cases," per Anthropic's own redeploy announcement). Confirmed current and generally available on Claude API, Claude Platform on AWS, Amazon Bedrock, Google Cloud, Microsoft Foundry, Claude.ai, Claude Code, and Claude Cowork. [Certain]
- `claude-mythos-5` shares identical capabilities/pricing/context but is Project Glasswing-only (invitation, no self-serve), lacks the safety classifiers Fable 5 carries, and succeeds the invitation-only Claude Mythos Preview.
- **Best for:** Anthropic's own description — "next-generation intelligence for long-running agents"; most capable widely-released model, for the most demanding reasoning and long-horizon autonomous agentic work (overnight coding runs, deep multi-step research, enterprise-scale deliverables). Not intended for research-biology or most cybersecurity-content workloads (safety classifiers specifically target that).
- **Context window:** 1M tokens (default, also the max). Max output: 128K tokens. Uses the same tokenizer as Opus 4.8/4.7 — token counts roughly unchanged vs Opus 4.7/4.8, but ~30% higher than pre-4.7-tokenizer models for the same text.
- **Thinking:** Extended thinking = No; Adaptive thinking = Yes, **always on** (cannot be disabled — `thinking: {type:"disabled"}` returns 400; omit the param instead). Raw chain-of-thought is never returned regardless of `display` setting.
- **API price:** $10/MTok input, $50/MTok output — **the most expensive model in the catalog**, exactly 2x Opus 4.8's per-token rate. [Certain]
- **Max-plan quota weight — the key ask:**
  - For Pro/Max/Team/select-Enterprise plans, Fable 5 was included for up to **50% of weekly usage limits through 2026-07-07**; after that window it moves to metered usage-credit billing at the $10/$50 per-MTok rate rather than being absorbed into the flat subscription pool. **That window has now passed relative to today (2026-07-06 is one day before cutover) — reverify current billing treatment before assuming flat-pool inclusion still applies going forward.** [Certain that the 50%-through-July-7 window existed; UNVERIFIED what applies after]
  - **Multiplier vs Opus:** Multiple converging sources (including a direct quote of Anthropic's own in-app UI copy: "Uses your limits ~2x faster than Opus") state Fable 5 burns the Max plan's 5-hour and weekly windows at **~2x the rate of Opus 4.8** for equivalent work — consistent with its 2x-Opus per-token API price. **[UNVERIFIED against a primary Anthropic support-doc page]** — no independent confirmation on `support.claude.com` or `anthropic.com` directly; Anthropic's public usage-limits pages describe the *structure* of limits but do not publish exact per-model multipliers. Corroborated by several independent trackers plus a quoted in-product UI string — treat as [Likely], not [Certain].
  - Requires 30-day data retention (not available under Zero Data Retention) — both Fable 5 and Mythos 5 are Anthropic "Covered Models" for this purpose.

---

## Max plan quota architecture (applies to all models, confirmed live)

- **5-hour rolling session limit**, shared across Claude.ai chat and Claude Code, doubled 2026-05-06 for Pro/Max/Team/seat-based Enterprise; peak-hour reductions removed.
- **Two weekly caps**: (1) an all-models aggregate weekly cap, (2) a **Sonnet-only** weekly sub-cap. Opus has its own separately-tracked reset schedule for monitoring purposes, but the only formally separate documented *cap* is the Sonnet one.
- Anthropic explicitly does **not** publish exact numeric hour/token figures for these caps anymore — any source (including many blogs) quoting fixed numbers like "40 hours Opus/week" or "480 hours Sonnet/week" should be treated as stale/unverified; the only stable, current facts are the *structure* (session + 2 weekly caps) and relative model cost-ordering (Opus > Sonnet > Haiku per-turn; Fable 5 > Opus, ~2x).
- Max 5x = $100/mo = 5x Pro's per-session usage budget; Max 20x = $200/mo = 20x Pro's budget. This multiplier is against the Pro baseline, not model-specific.

---

## Sources

- https://platform.claude.com/docs/en/about-claude/models/overview.md (models table, pricing, context/output, thinking support — fetched live)
- https://platform.claude.com/docs/en/about-claude/models/introducing-claude-fable-5.md (Fable 5 / Mythos 5 spec, availability, refusal/fallback/billing summary — fetched live)
- https://www.anthropic.com/news/redeploying-fable-5 (official redeploy announcement, safety classifier improvement, 2026-07-01 relaunch — fetched live)
- https://support.claude.com/en/articles/14552983-models-usage-and-limits-in-claude-code (Claude Code model cost-ordering guidance — fetched live)
- https://support.claude.com/en/articles/11049741-what-is-the-max-plan (Max plan tier structure, two-weekly-cap structure — fetched live)
- https://support.claude.com/en/articles/9797557-usage-limit-best-practices (fetched live; confirms model choice affects usage but does not publish multipliers)
- Third-party corroboration for the ~2x Fable-5-vs-Opus quota multiplier (not independently primary-source confirmed): developersdigest.tech, claudefa.st, fable5.app — converge on "2x" and quote an in-app Anthropic UI string ("Uses your limits ~2x faster than Opus")
