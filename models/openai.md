# OpenAI / Codex — Deep Detail

Verified July 2026, updated 2026-07-09. See `models/README.md` for the master matrix; see `models/models.yaml` for the machine-readable entries.

Cascade account: **C1**. Dispatch: Codex CLI.

**2026-07-09 update:** GPT-5.6 (Sol/Terra/Luna) flipped from restricted partner preview to public GA following US Dept. of Commerce clearance. Available via API and Codex. See dedicated section below for pricing and Cascade tier mapping.

---

## GA (generally available) today via `codex` CLI / ChatGPT subscription

| Model | Model id | Best for (Codex/coding context) | Context window (in Codex) | API context window | Notes |
|---|---|---|---|---|---|
| **GPT-5.5** (current flagship) | `gpt-5.5` (snapshot `gpt-5.5-2026-04-23`) | Primary Codex driver: large refactors, holding context across big systems, ambiguous-failure debugging, agentic tool-checking. [Certain] | **400K published cap**; CLI reserves ~5% headroom so usable is ~258-272K input + 128K output in practice (Codex splits 272K in / 128K out = 400K total). Several open GitHub issues (openai/codex #19319, #19409, #19464) show Codex's context accounting mismatches the API's 1M figure — treat 400K as the real Codex-session ceiling, not 1M. [Likely] | 1,050,000 tokens (API only) | No separate `gpt-5.5-codex` fine-tune this generation (earlier gens 5.1-5.3 had dedicated `-codex` variants; 5.5 uses the base model directly in Codex per the launch post). Knowledge cutoff Dec 1, 2025. Released Apr 23, 2026. |
| GPT-5.4 | `gpt-5.4` | Cheaper/faster fallback tier below 5.5, still capable coding model | 400K (Codex) | 1M (API), 128K max output | Still selectable in Codex alongside 5.5 |
| GPT-5.4 mini | `gpt-5.4-mini` | High-volume/cheap tasks, quick edits, low-stakes automation | Smaller (400K API-side) | 400K | Cheapest tier on Plus/Pro plans |
| GPT-5.3-Codex-Spark | `gpt-5.3-codex-spark` | Pro research-preview fast coding | — | — | Pro-only research preview, not broadly GA [Likely] |
| GPT-5.3-Codex | `gpt-5.3-codex` | Legacy coding | 400K | 400K | Deprecated for ChatGPT sign-in |
| GPT-5.2 | `gpt-5.2` | Legacy coding | 200K | 200K | Deprecated for ChatGPT sign-in |

**GPT-5.5 pricing (API, per 1M tokens):** $5.00 input / $0.50 cached-input / $30.00 output. Prompts >272K input tokens billed at 2x input / 1.5x output for the whole session. (developers.openai.com/api/docs/models/gpt-5.5)

---

## GPT-5.6 (Sol / Terra / Luna) — GA (2026-07-09)

Announced 2026-06-25/26 as a restricted partner preview (openai.com/index/previewing-gpt-5-6-sol), then flipped to **public GA on 2026-07-09** after US Dept. of Commerce clearance. New naming convention: the number (5.6) is the generation; Sol/Terra/Luna are durable **capability tiers** that will advance independently of the generation number going forward.

| Model | Model id | Positioning | Context window | Pricing (per 1M tok) | Cascade tier |
|---|---|---|---|---|---|
| GPT-5.6 Sol | `gpt-5.6-sol` | Flagship — hardest problems, complex coding, security research, final gates, adversarial CR | ~1.5M tokens (reported, up ~43% from 5.5 Pro's 1.05M) [Likely, single-source] | $5 in / $30 out | **T1** (Opus-class) |
| GPT-5.6 Terra | `gpt-5.6-terra` | Balanced — high-volume business tasks, bulk exec, internal tools, doc analysis | not officially published [Guessing]; assume 400K Codex ceiling like 5.5 until confirmed | $2.50 in / $15 out — ~2x cheaper than 5.5 at competitive perf | **T2** (Sonnet-class, bulk exec) |
| GPT-5.6 Luna | `gpt-5.6-luna` | Fast/cheap — summarization, drafting, routine automation, triage, grunt | not officially published [Guessing] | $1 in / $6 out — cheapest 5.6 tier | **T3** (Haiku-class, cheap grunt/triage) |

**Availability status:**

- GA rollout confirmed 2026-07-09 — public availability via **API and Codex**, cleared in coordination with the US government after the initial ~20-trusted-partner preview (per VentureBeat and OpenAI's own preview post). [Certain]
- Verify `codex --model gpt-5.6-sol` (and `-terra`/`-luna`) selectability on C1's specific plan tier before routing production traffic — GA does not guarantee every plan tier surfaces every model immediately. [Likely]
- A Tech Times report (2026-06-29) had claimed GPT-5.6 was "silently rolled out to some Codex users" ahead of the official GA date via a leaked/hidden system prompt reference — this pre-GA rumor is now superseded by the confirmed 2026-07-09 GA. [Certain — GA date confirmed]
- **Bottom line: GPT-5.6 Sol/Terra/Luna are GA as of 2026-07-09 and routable.** Cascade maps Sol->T1, Terra->T2, Luna->T3. `gpt-5.5` remains the documented fallback (see GA table above) until 5.6 selectability is independently confirmed stable on C1.

---

## Codex subscription plans & usage limits (July 2026)

| Plan | Price | Rate-limit tier | Models available |
|---|---|---|---|
| Free | $0/mo | Very limited | Basic Codex exploration only |
| Go | $8/mo | Base | GPT-5.5, 5.4, 5.4-mini |
| Plus | $20/mo | Base (5-hr window: ~15-80 msgs on 5.5) | GPT-5.5, 5.4, 5.4-mini |
| Pro | $100/mo (5x) or $200/mo (20x) | 5x: ~75-400 on 5.5; 20x: ~300-1,600 on 5.5 | + GPT-5.3-Codex-Spark (research preview) |
| Business | $20-25/user/mo (min 2 users) | Same tier as Plus | GPT-5.5, 5.4, 5.4-mini |
| Enterprise/Edu | Custom | Custom | Same, org-wide controls |

Usage is metered in rolling 5-hour windows with message-count ranges depending on task complexity (ranges above are approximate published bands, not fixed counts). Codex CLI itself is free software; billing flows through ChatGPT plan sign-in — no separate CLI license fee. Real-world active-developer cost commonly runs ~$100-200/mo per the pricing writeups (third-party estimate, not an OpenAI figure). [Likely]

**Codex plan tier (C1):** NOT recoverable from the local CLI — `~/.codex/auth.json` holds only a Google-OAuth `sub` (no plan/tier claim), and there is no `codex` status subcommand that exposes it (verified 2026-07-07). Confirm via the OpenAI/ChatGPT account dashboard (web) if needed; this file assumes Plus/Pro.

**gpt-5.6 plan-tier gating not yet confirmed [Guessing]:** the table above predates the 2026-07-09 GA flip and does not yet list gpt-5.6-sol/-terra/-luna per plan tier. Verify which plans surface 5.6 selection in the Codex CLI before relying on it for C1.

---

## Sources

- https://openai.com/index/introducing-gpt-5-5/
- https://developers.openai.com/api/docs/models/gpt-5.5
- https://openai.com/index/gpt-5-5-system-card/
- https://developers.openai.com/codex/pricing
- https://developers.openai.com/codex/changelog
- https://openai.com/index/previewing-gpt-5-6-sol/
- https://help.openai.com/en/articles/20001325-a-preview-of-gpt-56-sol-terra-and-luna (403 on direct fetch, content via search cache)
- https://venturebeat.com/technology/openai-unveils-gpt-5-6-sol-terra-and-luna-models-but-only-accessible-to-limited-preview-partners-for-now-per-us-gov
- https://community.openai.com/t/introducing-gpt-5-6-series-sol-terra-and-luna/1384931
- https://www.techtimes.com/articles/319297/20260629/openai-silently-rolled-gpt-56-some-codex-users-hidden-prompt-exposes-swap.htm (unverified rumor, flagged)
- https://github.com/openai/codex/issues/19319, #19409, #19464 (Codex context-window accounting bugs)
- https://x.com/OpenAI/status/2070555272230384038
