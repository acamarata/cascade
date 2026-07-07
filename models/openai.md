# OpenAI / Codex — Deep Detail

Verified July 2026. See `models/README.md` for the master matrix; see `models/models.yaml` for the machine-readable entries.

Cascade account: **C1**. Dispatch: Codex CLI.

---

## GA (generally available) today via `codex` CLI / ChatGPT subscription

| Model | Model id | Best for (Codex/coding context) | Context window (in Codex) | API context window | Notes |
|---|---|---|---|---|---|
| **GPT-5.5** (current flagship) | `gpt-5.5` (snapshot `gpt-5.5-2026-04-23`) | Primary Codex driver: large refactors, holding context across big systems, ambiguous-failure debugging, agentic tool-checking. [Certain] | **400K published cap**; CLI reserves ~5% headroom so usable is ~258-272K input + 128K output in practice (Codex splits 272K in / 128K out = 400K total). Several open GitHub issues (openai/codex #19319, #19409, #19464) show Codex's context accounting mismatches the API's 1M figure — treat 400K as the real Codex-session ceiling, not 1M. [Likely] | 1,050,000 tokens (API only) | No separate `gpt-5.5-codex` fine-tune this generation (earlier gens 5.1-5.3 had dedicated `-codex` variants; 5.5 uses the base model directly in Codex per the launch post). Knowledge cutoff Dec 1, 2025. Released Apr 23, 2026. |
| GPT-5.4 | `gpt-5.4` | Cheaper/faster fallback tier below 5.5, still capable coding model | 400K (Codex) | 1M (API), 128K max output | Still selectable in Codex alongside 5.5 |
| GPT-5.4 mini | `gpt-5.4-mini` | High-volume/cheap tasks, quick edits, low-stakes automation | Smaller (400K API-side) | 400K | Cheapest tier on Plus/Pro plans |
| GPT-5.3-Codex-Spark | (research preview) | Experimental fast-iteration coding variant | — | — | Pro-only research preview, not broadly GA [Likely] |

**GPT-5.5 pricing (API, per 1M tokens):** $5.00 input / $0.50 cached-input / $30.00 output. Prompts >272K input tokens billed at 2x input / 1.5x output for the whole session. (developers.openai.com/api/docs/models/gpt-5.5)

---

## GPT-5.6 (Sol / Terra / Luna) — ANNOUNCED, PREVIEW-ONLY, NOT PUBLICLY GA

Announced 2026-06-25/26 (openai.com/index/previewing-gpt-5-6-sol). This is a **limited/restricted preview**, explicitly **not** a public ChatGPT or open Codex rollout:

| Model | Model id | Positioning | Context window | Pricing (per 1M tok) |
|---|---|---|---|---|
| GPT-5.6 Sol | `gpt-5.6-sol` | Flagship — hardest problems, complex coding, security research | ~1.5M tokens (reported, up ~43% from 5.5 Pro's 1.05M) [Likely, single-source] | $5 in / $30 out |
| GPT-5.6 Terra | `gpt-5.6-terra` | Balanced — high-volume business tasks, internal tools, doc analysis | not officially published [Guessing] | $2.50 in / $15 out |
| GPT-5.6 Luna | `gpt-5.6-luna` | Fast/cheap — summarization, drafting, routine automation | not officially published [Guessing] | $1 in / $6 out |

**Availability status — flag clearly:**

- Available only through the **API and Codex to ~20 trusted partner organizations**, cleared in coordination with the US government (per VentureBeat and OpenAI's own preview post). [Certain]
- **Not available to normal ChatGPT/Codex subscribers** (Free/Go/Plus/Pro/Business) as of July 2026. OpenAI states plans to broaden ChatGPT/Codex/API access "in the coming weeks" — no confirmed GA date found. [Certain — per OpenAI's own preview announcement]
- A Tech Times report (2026-06-29) claims GPT-5.6 was "silently rolled out to some Codex users" via a leaked/hidden system prompt reference — this is a third-party/unverified claim, not an OpenAI-confirmed rollout. Treat as [Guessing]/rumor, not GA. Some Codex CLI users report `/status` showing a 353K context window consistent with 5.6 access, but this is community-reported, unconfirmed by OpenAI. [Guessing]
- **Bottom line: GPT-5.6 is announced, in restricted preview, NOT publicly GA as of July 2026.** Do not treat it as selectable for a normal subscription. Cascade must not route to `gpt-5.6-*` model IDs — they are not reachable from C1's plan tier.

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

**Action item:** verify which Codex plan tier C1 is actually on — this file assumes Plus/Pro but that has not been independently confirmed against Cascade's account config.

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
