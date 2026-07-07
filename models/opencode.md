# OpenCode / z.ai (GLM) — Deep Detail

Verified July 2026. See `models/README.md` for the master matrix; see `models/models.yaml` for the machine-readable entries.

Cascade account: **OpenCode Run**. Dispatch: OpenCode CLI.

---

## TLDR

- **GLM-5.2 confirmed available two ways**: (1) bundled in **OpenCode Go** ($10/mo) and pay-per-token on **OpenCode Zen**; (2) directly via **z.ai GLM Coding Plan** (subscription) or z.ai API (per-token). Also on OpenRouter/Together/Fireworks as third paths. [Certain]
- **Go = subscription roster of 13-14 open-weight coding models** with pooled $-based rate limits ($12/5h, $30/wk, $60/mo) for $10/mo ($5 first month). **Zen = pay-as-you-go superset** (curated 50-74+ models incl. Claude/GPT/Gemini) billed per token, no subscription. They are different products on the same OpenCode account system.
- Best picks: **GLM-5.2** = best all-round flagship reasoning+long-horizon agentic coding; **DeepSeek V4 Pro** = best raw coding/SWE-bench value; **Qwen3.7 Max** = best long-context (1M) + agent tool-use marathon runs; **DeepSeek V4 Flash / MiMo-V2.5** = best cheap+fast bulk/triage; **MiniMax M3** = best long-context + native multimodal; **Kimi K2.7 Code** = best token-efficient large-codebase agentic coding.

---

## 1. OpenCode Go vs OpenCode Zen — structural difference

| | **OpenCode Go** | **OpenCode Zen** |
|---|---|---|
| Model | Flat subscription | Pay-as-you-go, per-1M-token pricing |
| Price | $5 first month, then **$10/mo** | No subscription fee; $20 initial pay-as-you-go balance, auto-reloads $20 when balance <$5 |
| Limits | $-equivalent pooled caps: **$12 / 5hr, $30 / week, $60 / month** (translates to model-dependent request counts) | No pooled cap — pure metered billing per request |
| Roster | 13-14 curated **open-weight** coding models only (GLM, DeepSeek, Qwen, Kimi, MiMo, MiniMax) | Much larger curated set (50-74+ models depending on source) including **Claude, GPT, Gemini, Grok** plus the same open-weight lineup, plus rotating **free promotional models** |
| Best for | Predictable flat-fee heavy daily coding-agent use on open models | Occasional/mixed use across proprietary + open models, or when Claude/GPT/Gemini access billed per-token through the same account is wanted |
| Sources | https://opencode.ai/docs/go/ · https://opencode.ai/go · https://www.bitdoze.com/opencode-go-plan/ | https://opencode.ai/docs/zen/ · https://opencode.ai/zen · https://opencode.ai/zen/v1/models |

[UNVERIFIED]: exact current total model count on Zen — sources disagree (49 vs 74); treat as "50+, fluctuating, some promotional/rotating."

---

## 2. Full OpenCode Go roster + what each is best at

| Model | Best at | Requests/5hr (Go pool) | Notes / source |
|---|---|---|---|
| **GLM-5.2** (Zhipu/z.ai) | **Flagship reasoning + long-horizon agentic coding.** 1M context, dual thinking effort (High/Max), MIT open weights. SWE-bench Pro 62.1 (beats GPT-5.5 58.6), FrontierSWE 74.4% (near Opus 4.8's 75.1%), DeepSWE 46.2 vs GLM-5.1's 18.0, Terminal-Bench 2.1 jumped 62.0->81.0. Best pick when task needs sustained multi-hour autonomous coding/refactors. | ~880 (most expensive in pool) | huggingface.co/blog/zai-org/glm-52-blog · venturebeat.com/.../z-ais-open-weights-glm-5-2 · codingfleet.com/blog/glm-5-2-vs-glm-5-1 |
| **GLM-5.1** | Prior-gen flagship reasoning; solid but superseded by 5.2 on every published benchmark (0 wins vs 5.2's 11). Use only if 5.2 quota exhausted. | ~880-4,300 | codingfleet.com/blog/glm-5-2-vs-glm-5-1 |
| **DeepSeek V4 Pro** | **Best raw coding + reasoning value.** 1.6T MoE (49B active), 1M context default. LiveCodeBench 93.5% (beats Claude 88.8%), SWE-bench Verified 80.6%, GPQA Diamond 90.1% (V4-Pro-Max), Terminal-Bench 2.0 67.9. Weak spot: factual recall/HLE (37.7 vs Claude's 40.0). Best for: general coding, algorithm work, long-context refactors at ~1/30th frontier cost. | ~3,450 | codersera.com/blog/deepseek-v4-pro-review-benchmarks-pricing-2026 · datacamp.com/blog/deepseek-v4 |
| **DeepSeek V4 Flash** | **Cheapest + fastest, best for bulk/triage/simple tasks.** $0.14/$0.28 per 1M tokens standalone pricing. Use for high-volume mechanical work, quick edits, classification — not deep reasoning. | ~31,650 (cheapest, highest volume) | opencode.ai/docs/go · aipricing.guru |
| **Qwen3.7 Max** | **Best long-context (1M) + marathon autonomous agent runs.** SWE-bench Verified 80.4, Terminal-Bench 2.0 69.7 (beats DeepSeek V4 Pro's 67.9 and Opus-4.6 Max's 65.4), MRCR-v2 128k long-context retrieval 90.4 (best in class). Demonstrated 35-hour autonomous session, 1,158 tool calls. Best for: huge codebase context loads, very long agent sessions. | mid-tier | datacamp.com/blog/qwen3-7-max · buildfastwithai.com/blogs/qwen-3-7-max-review-2026 |
| **Qwen3.7 Plus** | Mid-tier general purpose, cheaper than Max, good default for everyday tasks needing decent context without Max's cost. | higher volume than Max | inferred from tiering pattern; [UNVERIFIED] exact benchmark split vs Max |
| **Qwen3.6 Plus** | Reliable previous-gen mid-tier; fallback if 3.7 unavailable/rate-limited. | ~3,300 | bitdoze.com/opencode-go-plan |
| **Kimi K2.7 Code** | **Best token-efficiency on large agentic coding runs.** +21.8% over K2.6 on Kimi Code Bench v2, +31.5% on MLS Bench Lite, 81.1 on MCP Mark Verified (beats Opus 4.8 there), cuts reasoning tokens ~30%. Caveat: benchmarks are Moonshot-proprietary, no independent SWE-bench-Verified/LiveCodeBench numbers yet. Best for: cost-sensitive long agentic coding sessions where token efficiency matters more than peak capability. | mid-tier | flowtivity.ai/blog/kimi-k2-7-complete-review · kingy.ai/ai/kimi-k2-7-code-benchmarks-specs |
| **Kimi K2.6** | 1T MoE (32B active), 262K context, Agent Swarm (300 sub-agents, 4,000 steps). Strong Next.js/web-stack coding (+50% vs K2.5), 96.6% tool-invocation success. Good general agentic coding baseline before K2.7. | ~1,150 | huggingface.co/moonshotai/Kimi-K2.6 · verdent.ai/guides/what-is-kimi-k2-6 |
| **MiMo-V2.5-Pro** (Xiaomi) | **Best token-efficiency/cost ratio at near-frontier quality.** SWE-bench Pro 57.2, ClawEval 63.8/64% Pass3, tau3-Bench 72.9 — matches frontier closed models while using 40-60% fewer tokens per trajectory than Opus 4.6/Gemini 3.1 Pro/GPT-5.4. Best for: agentic coding where frontier-adjacent quality is wanted but minimal token spend. | ~1,290 | marktechpost.com/.../xiaomi-releases-mimo-v2-5-pro · kilo.ai/models/xiaomi-mimo-v2-5-pro |
| **MiMo-V2.5** (base) | Cheaper non-Pro variant ($0.10/1M tokens standalone), Pareto-frontier efficiency on ClawEval (62.3). Good budget default for lighter coding tasks. | ~2,150 | kilo.ai/models/xiaomi-mimo-v2-5 |
| **MiniMax M3** | **Best long-context (1M, MSA sparse attention) + native multimodality** (image/video input, can operate a desktop). SWE-Bench Pro 59.0% (beats GPT-5.5 and Gemini 3.1 Pro, approaches Opus 4.7), BrowseComp 83.5% (beats Opus 4.7). Best for: multimodal agentic tasks, browser/desktop automation, very large context needs. | mid-tier, newest | minimax.io/blog/minimax-m3 · fireworks.ai/blog/minimax-m3-launch |
| **MiniMax M2.7** | Prior-gen, 200K context, SWE-Bench Pro 56.2%. Solid agentic-tasks default (one reviewer called it "go-to for agentic tasks"); cheaper than M3. | ~3,400-6,300 | lushbinary.com/blog/minimax-m3-vs-m2-7 · artificialanalysis.ai/models/minimax-m2-7 |

Note: request-count figures vary slightly between sources (opencode.ai docs vs bitdoze.com review) since OpenCode periodically rebalances the pool — treat exact numbers as approximate/[UNVERIFIED] snapshot, the ranking (Flash cheapest -> GLM-5.2 priciest) is [Certain].

---

## 3. z.ai GLM Coding Plan (direct path, separate from OpenCode)

| Tier | Promo price (through Sept 2026) | List price | Limits |
|---|---|---|---|
| Lite | $12.60/mo ($126/yr promo, $151.20/yr list) | $18/mo | ~80 prompts/5hr, ~400/week, 100 MCP calls/mo |
| Pro | $50.40/mo | $72/mo | ~400 prompts/5hr, ~2,000/week, 1,000 MCP calls/mo |
| Max | $112/mo | $160/mo | ~1,600 prompts/5hr, ~8,000/week, 4,000 MCP calls/mo |

All three tiers include the same model lineup: **GLM-5.2** (flagship), **GLM-5-Turbo** (speed-optimized: 262K context, 48 tok/s throughput, $1.20/$4.00 per 1M, best for fast agent-chain execution where latency matters more than peak reasoning), **GLM-4.7**, **GLM-4.5-air** (older/lighter tiers). Works inside Claude Code, Cline, Roo Code, OpenClaw, 20+ clients — not exclusive to z.ai's own tools.
Sources: z.ai/subscribe · docs.z.ai/guides/overview/pricing · venturebeat.com/.../z-ai-debuts-faster-cheaper-glm-5-turbo

**z.ai API (metered, no subscription):** GLM-5.2 = ~$0.91 input / $2.86 output per 1M tokens (35% promo pricing per OpenRouter listing) or $0.9086/$2.856 unrounded; list price elsewhere cited as ~$0.95/$3.00 blended non-promo. 1M context, high/xhigh reasoning effort levels. Source: openrouter.ai/z-ai/glm-5.2, docs.z.ai/guides/llm/glm-5.2.

---

## 4. GLM-5.2 availability paths (confirmed)

1. **OpenCode Go** — bundled in the $10/mo subscription roster (flagship slot).
2. **OpenCode Zen** — pay-per-token, same OpenCode account, no subscription.
3. **z.ai direct** — GLM Coding Plan subscription (Lite/Pro/Max) OR z.ai metered API.
4. **Third-party routers** — OpenRouter, Together AI (`zai-org/GLM-5.2`), Fireworks, Hugging Face Inference, NVIDIA, Cloudflare Workers AI, Vercel AI Gateway, DeepInfra (cited as one of the cheapest routes).

Source: developersdigest.tech/blog/glm-5-2-free-and-cheap-access-2026 · together.ai/models/glm-52 · z.ai/model-api

---

## 5. Practical picker (by task)

| Task | Pick | Why |
|---|---|---|
| Deep multi-hour agentic refactor / hardest reasoning | **GLM-5.2** | Best benchmarks across the board, 1M context, MIT open |
| Best coding $/token value | **DeepSeek V4 Pro** | ~1/30th frontier cost, near-Claude/GPT coding benchmarks |
| Bulk mechanical edits, triage, cheap high-volume | **DeepSeek V4 Flash** or **MiMo-V2.5 (base)** | Cheapest, highest throughput in the pool |
| Longest context / marathon autonomous sessions | **Qwen3.7 Max** or **MiniMax M3** | Both true 1M context, best long-context retrieval scores |
| Multimodal (image/video/desktop control) | **MiniMax M3** | Only one in the roster with native multimodality |
| Token-efficiency at near-frontier quality | **MiMo-V2.5-Pro** | 40-60% fewer tokens/trajectory than Opus/Gemini/GPT at similar quality |
| Fast low-latency agent chains | **GLM-5-Turbo** (z.ai direct, not in OpenCode Go roster) | Purpose-built for speed (48 tok/s), not in Go's list — needs z.ai path |
| Large-codebase / Next.js-heavy agentic coding | **Kimi K2.7 Code** or **Kimi K2.6** | Strong web-stack coding gains, Agent Swarm orchestration |

---

## Gaps / [UNVERIFIED] flags

- Exact live Zen total model count (49 vs 74 across sources) — fluctuates, some models promotional/rotating (e.g. "Big Pickle" stealth model, free-tier models).
- Exact current request-count-per-5hr numbers for Go pool vary slightly between opencode.ai docs snapshot and third-party review (bitdoze.com) — likely reflects periodic rebalancing; directional ranking is solid, absolute numbers are a point-in-time snapshot.
- Qwen3.7 Plus / Qwen3.6 Plus detailed independent benchmarks not directly sourced (inferred mid-tier positioning from naming/pricing pattern only).
