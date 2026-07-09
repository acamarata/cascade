# Cascade Model Reference

Canonical, human-readable reference for "which subscription has which model" and "which model is best for what," used by Cascade agents and the daemon's routing logic. Machine-readable twin: `models.yaml`. Per-provider deep detail: `anthropic.md`, `openai.md`, `google.md`, `opencode.md`.

**Verified:** 2026-07-06, via live fetches of provider docs (platform.claude.com, developers.openai.com, ai.google.dev, opencode.ai, z.ai — see per-provider files for exact source URLs). This is not from training memory.

**Cross-reference check:** `crates/cascade-core/src/model_ids.rs` holds the Rust constants Cascade's code-generation layer actually imports (`MODEL_CLAUDE_OPUS`, `MODEL_CLAUDE_SONNET`, `MODEL_CLAUDE_HAIKU`, `MODEL_CLAUDE_FABLE`, `MODEL_GPT`, `MODEL_GEMINI_PRO`, `MODEL_GEMINI_FLASH`, `MODEL_GLM`). As of this write **all eight constants are current** — `claude-opus-4-8`, `claude-sonnet-5`, `claude-haiku-4-5`, `claude-fable-5`, `gpt-5.5`, `gemini-3.1-pro`, `gemini-3.5-flash`, `glm-5.2` all match this research's GA findings. No drift found. The live fleet-routing matrix (`accounts_store::build_model_matrix()`) is populated at runtime from `~/.cascade/accounts/accounts.json`, not hardcoded, so `model_ids.rs` is the correct static-source-of-truth to diff against on future updates.

---

## Master Matrix

| Model | Provider | Sub / Account | Cascade lane --account | Best for | Quota cost-weight | Context | GA / Preview |
|---|---|---|---|---|---|---|---|
| Claude Opus 4.8 | Anthropic | Max | T1 --A1/A2 | Architectural decisions, final gates, CR-C, hardest synthesis | Baseline reference; 2nd most expensive of routine tiers | 1M / 128K out | **GA** |
| Claude Sonnet 5 | Anthropic | Max | T2 --A1/A2 (DEFAULT_HARNESS_MODEL) | Bulk exec, code gen, CR-A/B, QA, day-to-day coding | Cheapest frontier tier; own dedicated weekly sub-cap | 1M / 128K out | **GA** (intro price to 2026-08-31) |
| Claude Haiku 4.5 | Anthropic | Max | T3 --A1/A2 | Triage, grunt work, post-prompt hooks | Cheapest of the four Claude models | 200K / 64K out | **GA** |
| Claude Fable 5 | Anthropic | Max (Fable-included window) | T1-plus, hardest reasoning only | Overnight autonomous runs, deep research, enterprise deliverables | ~2x Opus burn [Likely]; 2x Opus API price exactly | 1M / 128K out | **GA** (redeployed 2026-07-01; 50%-of-weekly-limit window ended 2026-07-07 — reverify billing) |
| GPT-5.5 | OpenAI | Codex (C1) | T2 --C1 | Large refactors, cross-system context, ambiguous-failure debugging | $5in/$30out per MTok; 2x/1.5x beyond 272K input | 400K usable in Codex (1.05M API) | **GA** |
| GPT-5.4 / 5.4-mini | OpenAI | Codex (C1) | T2-fallback / T3 | Cheaper fallback / high-volume cheap tasks | Lower than 5.5 | 400K (Codex) | **GA** |
| GPT-5.6 Sol/Terra/Luna | OpenAI | Restricted partner preview only | Not usable | Hardest problems (Sol); balanced biz (Terra); cheap/fast (Luna) | Published per-tier pricing, not billable to us | ~1.5M (Sol, reported) | **PREVIEW — NOT GA, NOT accessible on any normal plan** |
| Gemini 3.1 Pro | Google | Google AI Pro/Ultra (agy) | T2 --agy | Complex problem-solving, agentic/vibe coding | $2-4in/$12-18out per MTok, paid-only | unpublished | **GA** (preview-labeled by Google, paid-only) |
| Gemini 3.5 Pro | Google | Google AI Pro/Ultra (pending) | Not yet wired | Frontier reasoning + 2M-token corpus ingestion | unpublished | 2M | **PREVIEW — slipped to July 2026, NOT confirmed GA as of 2026-07-06** |
| Gemini 3.5 Flash | Google | GFP free pool (28 keys) + agy paid | T3 --GFP (backbone) | Always-on grunt work, post-prompting, classify/triage/summarize | **FREE** in free tier; paid $1.50in/$9.00out | unpublished | **GA** |
| Gemini 3.1 Flash-Lite | Google | GFP free pool | T3-cheap fallback | Cheapest possible grunt work | FREE; paid $0.25in/$1.50out | unpublished | **GA** |
| Gemini 2.5 Pro | Google | GFP free pool (contested) | Legacy fallback | Still free-tier eligible per official page [Guessing — conflicting evidence] | FREE (disputed by 3rd parties) | unpublished | **GA** |
| gemini-flash-latest | Google | GFP free pool (28 keys) + agy paid | T3 --GFP (backbone) | Always-on grunt work, post-prompting, classify/triage/summarize | **FREE** in free tier; paid $1.50in/$9.00out | 1M | **GA** (Canonical auto-tracking alias) |
| gemini-flash-lite-latest | Google | GFP free pool | T3-cheap fallback | Cheapest possible grunt work | FREE; paid $0.25in/$1.50out | 1M | **GA** (Canonical auto-tracking alias) |
| Gemini 2.0 Flash | Google | GFP free pool | T3-legacy | Legacy fallback / pinned | FREE; legacy / pinned — prefer gemini-flash-latest | 1M | **GA** (Legacy / pinned) |
| Gemini 2.5 Flash | Google | GFP free pool | T3-legacy | Legacy fallback / pinned | FREE; legacy / pinned — prefer gemini-flash-latest | 1M | **GA** (Legacy / pinned) |
| GLM-5.2 | Zhipu/z.ai | OpenCode Go/Zen, z.ai direct | T2 --OpenCode Run | Flagship reasoning, long-horizon agentic coding | ~880 req/5hr in Go pool (priciest slot); MIT open weights | 1M | **GA** |
| DeepSeek V4 Pro | DeepSeek (via OpenCode) | OpenCode Go/Zen | T2-value | Best raw coding $/token value | ~3,450 req/5hr; ~1/30th frontier API cost | 1M | **GA** |
| DeepSeek V4 Flash | DeepSeek (via OpenCode) | OpenCode Go/Zen | T3-cheap | Bulk mechanical edits, triage | ~31,650 req/5hr (cheapest, highest volume) | unpublished | **GA** |
| Qwen3.7 Max | Alibaba (via OpenCode) | OpenCode Go/Zen | T2-longctx | Longest context + marathon agent runs | mid-tier volume | 1M | **GA** |
| MiniMax M3 | MiniMax (via OpenCode) | OpenCode Go/Zen | T2-multimodal | Multimodal, browser/desktop automation | mid-tier volume, newest | 1M | **GA** |
| Kimi K2.7 Code | Moonshot (via OpenCode) | OpenCode Go/Zen | T2-efficient | Token-efficient large-codebase agentic coding | mid-tier volume | unpublished | **GA** |
| MiMo-V2.5-Pro | Xiaomi (via OpenCode) | OpenCode Go/Zen | T2-efficient | Frontier-adjacent quality, minimal token spend | ~1,290 req/5hr | unpublished | **GA** |
| GLM-5-Turbo | Zhipu/z.ai | z.ai direct only (NOT in Go roster) | latency-sensitive lane | Fast low-latency agent chains | $1.20in/$4.00out per MTok | 262K | **GA** |
| GPT-5.5 (Copilot) | GitHub | github-copilot | --copilot | GitHub issue/PR/review/CI | Included in Copilot sub | 1M | **GA** |
| Claude Fable 5 (Copilot) | Anthropic | github-copilot | --copilot | GitHub issue/PR/review/CI | Included in Copilot sub | 1M | **GA** |
| Claude Opus 4.8 (Copilot) | Anthropic | github-copilot | --copilot | GitHub issue/PR/review/CI | Included in Copilot sub | 1M | **GA** |
| Claude Sonnet 5 (Copilot) | Anthropic | github-copilot | --copilot | GitHub issue/PR/review/CI | Included in Copilot sub | 1M | **GA** |
| Claude Haiku 4.5 (Copilot) | Anthropic | github-copilot | --copilot | GitHub issue/PR/review/CI | Included in Copilot sub | 200K | **GA** |
| Gemini 3.5 Flash (Copilot) | Google | github-copilot | --copilot | GitHub issue/PR/review/CI | Included in Copilot sub | 1M | **GA** |
| Gemini 3.1 Pro (Copilot) | Google | github-copilot | --copilot | GitHub issue/PR/review/CI | Included in Copilot sub | 1M | **GA** |
| Kimi K2.7 Code (Copilot) | Moonshot | github-copilot | --copilot | GitHub issue/PR/review/CI | Included in Copilot sub | 1M | **GA** |
| Devin Agent | Devin | devin | --devin | multi-agent desktop+cloud | Included in Devin sub | 1M | **GA** (ex-Windsurf) |
| Cursor Agent | Cursor | cursor-cli | --cursor | IDE/agent UX | Included in Cursor sub | 1M | **GA** |
| GLM-5.2 (z.ai) | Zhipu/z.ai | zai-coding-plan | --zai | flagship coding/long-context | Included in z.ai GLM Coding Plan | 1M | **GA** |
| GLM-5-Turbo (z.ai) | Zhipu/z.ai | zai-coding-plan | --zai | latency lane | Included in z.ai GLM Coding Plan | 262K | **GA** |

Full per-model detail, benchmarks, and source citations: `anthropic.md` · `openai.md` · `google.md` · `opencode.md`. Machine-readable: `models.yaml`.

---

## New Subscriptions (2026-07 Audit)

- **github-copilot**: Supported models include GPT-5.5, GPT-5.3-Codex, GPT-5.4/mini/nano, Claude Fable 5, Opus 4.8, Sonnet 5, Haiku 4.5, Gemini 3.5 Flash, Gemini 3.1 Pro, and Kimi-K2.7-Code. Note that Grok Code Fast 1 was retired in Copilot on 2026-05-15. Best for GitHub issue/PR/review/CI.
- **devin**: Ex-Windsurf. Available via Desktop/CLI/Cloud/API. Best for multi-agent desktop+cloud.
- **cursor-cli**: Official CLI + cloud agents. Best for IDE/agent UX.
- **zai-coding-plan**: Distinct endpoint. Includes GLM-5.2 (flagship coding/long-context) and GLM-5-Turbo (latency lane).

---

## Pick-by-Task Quick Reference

| Task | Pick | Why |
|---|---|---|
| **Coding (default day-to-day)** | Claude Sonnet 5 / GPT-5.5 | Best speed/intelligence combo, cheapest frontier tier, Cascade's `DEFAULT_HARNESS_MODEL` / primary Codex driver |
| **Hardest coding / Deep reasoning** | Claude Fable 5 / Opus 4.8 | Highest-capability reasoning and hardest coding synthesis |
| **GitHub PR / issue / review / CI** | GitHub Copilot | Integrated directly into GitHub workflows and CI |
| **Multi-agent desktop + cloud** | Devin | Ex-Windsurf; full agentic desktop and cloud environment |
| **IDE / agent UX** | Cursor | Official CLI + cloud agents for seamless IDE integration |
| **Cheap long-context** | GLM-5.2 / Kimi K2.7 Code / Qwen3.7 Max | True 1M context with strong long-context retrieval scores and token efficiency |
| **Fast grunt work / triage** | Gemini flash-latest | Free in free tier, auto-tracking, immune to retirements |
| **Coding, budget-value alternative** | DeepSeek V4 Pro (OpenCode) | ~1/30th frontier cost, SWE-bench Verified 80.6%, beats Claude on LiveCodeBench |
| **Multimodal / desktop automation** | MiniMax M3 (OpenCode) | Only roster model with native image/video/desktop-control multimodality |
| **Fast low-latency agent chains** | GLM-5-Turbo (z.ai direct) | 48 tok/s, purpose-built for latency — requires z.ai path, not in OpenCode Go |
| **Bulk mechanical edits at max volume** | DeepSeek V4 Flash or Gemini 3.1 Flash-Lite | Cheapest per-request in their respective pools |
| **DO NOT ROUTE HERE (not GA)** | GPT-5.6 (Sol/Terra/Luna), Gemini 3.5 Pro | Announced but restricted-preview / not confirmed publicly GA as of 2026-07-06 |

---

## How Cascade Uses This

- **Model IDs are defined once** in `crates/cascade-core/src/model_ids.rs` and consumed by code-generation/harness layers (AGENTS.md, opencode.json, etc.). This `models/` directory is the research/reference layer that justifies and keeps those constants current — update `model_ids.rs` first when a provider ships a new GA flagship, then re-diff this directory.
- **Cascade Conductor** (`cascade conductor --tier T1/T2/T3`) routes delegated one-shot work quota-aware across accounts: worker spill order **A2 -> A1 spare -> Codex -> Gemini -> OC Go -> GP**, skipping any account that's auth-dead or at its 5h/7d cap. Tier-to-model mapping: T1 -> Opus, T2 -> Sonnet 5, T3 -> Haiku (or GP/Flash when routed there for near-free volume).
- **GP (Gemini Flash Pool)** is the preferred cheap workhorse for research/prep that front-loads context so an Opus/Sonnet finish is faster and cheaper — see GFP Backbone verdict below. Proxy runs at `:3761` (native Gemini format) and `:3762` (Anthropic-compat adapter, lets Claude Code's own model routing transparently redirect Haiku-tier calls to Flash for free).
- **OpenCode Go/Zen models** are the overflow lane when Anthropic quota (A1+A2) and Codex are both constrained — GLM-5.2 as the flagship-tier overflow, DeepSeek V4 Flash / MiMo-V2.5 for cheap-tier overflow.
- Never hardcode a model name outside `model_ids.rs` in Rust code — this directory and that file are the only two places model IDs should be typed. Everywhere else, import the constant.

---

## GFP Backbone — Verdict

**Should the free Gemini Flash pool (28-key/28-project rotation) be the always-on grunt-work/post-prompting backbone? Yes, with two monitored risks — not an unconditional "unlimited" assumption.**

- **Quality:** Gemini 3.5 Flash is reported (DeepMind/Appwrite/llm-stats) to *beat* Gemini 3.1 Pro (the prior-gen full Pro model) on coding/agentic benchmarks — a strong proxy signal that it's quality-equal-or-better than Claude Haiku for grunt work, though no direct independent Flash-vs-Haiku benchmark exists [Likely, not Certain]. For the GCI-defined GP use case (triage, classify, grep+summarize), even a materially weaker model would suffice, so quality risk here is low regardless.
- **Capacity is real but not "unlimited."** Per-project free-tier caps run low-hundreds-to-~1,500 requests/day and single-to-low-double-digit RPM — a single project would throttle fast. The 28-project rotation multiplies effective free throughput today, genuinely and technically. Call this **"high-volume-cheap," not "unlimited."**
- **Two risks to actively monitor, not assume away:**
  1. **ToS exposure** — Google's documented policy explicitly names "creating multiple projects to circumvent rate limits" as prohibited. Enforcement is described as scale-dependent (hobbyist rotation tolerated, production-scale abuse risks suspension) but this is a gray-zone workaround, not sanctioned architecture.
  2. **Policy-drift risk** — two tightening events in the last ~4 months (Mar 23 billing-account consolidation, Apr 1 Pro-model free-tier restriction) show the free-tier rules this pool depends on are actively narrowing. Build a paid fallback (Gemini 3.1 Flash-Lite at $0.10-0.25/M input is cheap enough to be a trivial fallback) rather than architecting as if the free pool is permanently guaranteed.
- **Model freshness:** the GP proxy currently targets `gemini-2.0-flash` — verified STILL LIVE on 2026-07-07 (returns 429 quota, not 404), so not an outage. It is older-gen, though; a future-proofing follow-up is to point the proxy at `gemini-flash-latest`/`gemini-flash-lite-latest` (auto-tracks Google's current flash, immune to retirements).
- **Recommended action:** re-verify actual per-project RPM/RPD live at `aistudio.google.com/rate-limit` against a real GFP project rather than trusting any published table — Google itself stopped publishing static numbers.

Full reasoning and sources: `google.md` § Strategy verdict.

---

## Update Protocol

1. Re-verify quarterly, or immediately when a provider announces a new flagship/GA model.
2. Fetch live from each provider's docs (not training memory) — platform.claude.com, developers.openai.com/api/docs, ai.google.dev/gemini-api/docs, opencode.ai/docs, z.ai docs.
3. Diff findings against `crates/cascade-core/src/model_ids.rs` — update the Rust constants first (that's what code actually imports), then update `models.yaml` and this README to match.
4. Flag anything announced-but-not-GA clearly (see GPT-5.6, Gemini 3.5 Pro in this version) — never let an agent route production traffic to a preview-only model ID that isn't reachable on the account's actual plan tier.
5. Tag every claim [Certain] / [Likely] / [Guessing] per GCI critical-thinking rules; carry forward [UNVERIFIED] flags rather than silently dropping them on refresh.
