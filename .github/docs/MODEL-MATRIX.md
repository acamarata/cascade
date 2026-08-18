# Model Matrix — Available Models per Subscription

Verified July 2026 against official sources (anthropic.com, openai.com, ai.google.dev,
z.ai, opencode.ai). Model IDs match Cascade's `crates/cascade-core/src/model_ids.rs`
constants (confirmed current). Update when a provider ships a new model or changes limits.

Related: [MODEL-ROUTING.md](MODEL-ROUTING.md) (how Conductor picks between these).

---

## Matrix

| Model ID | Provider | Sub / Account | Cascade lane (`--account`) | Context | Cost | Limits |
|---|---|---|---|---|---|---|
| `claude-opus-4-8` | Anthropic | Max (A1/A2) | `claude`, `claude2` | 1M API / 200k chat | Paid (Max) | weekly Opus sub-cap within combined weekly limit |
| `claude-sonnet-5` | Anthropic | Max (A1/A2) — **T2 default** | `claude`, `claude2` | 1M | Paid | 5h rolling + weekly cap (all-models + Sonnet sub-cap) |
| `claude-haiku-4-5` | Anthropic | Max (A1/A2) — T3 | `claude`, `claude2` | 200k | Paid | folds into same 5h/weekly pool |
| `claude-fable-5` | Anthropic | Max (A1/A2) — Mythos-class | `claude`, `claude2` | 1M | Paid (incl. Pro/Max) | GA 2026-07-01; own weekly allowance |
| `gpt-5.5` | OpenAI | ChatGPT Plus/Pro (Codex CLI) | `codex-acc1` | 1M API / capped in CLI | Paid (ChatGPT sub) | no separate `-codex` id; CLI caps window |
| `gemini-3.1-pro` | Google | **Google AI Pro** ($20/mo Google One) | `gemini-acc1` (via agy/cloudcode-pa) | 1M | Paid | newest Pro model (NOT "3.5 Pro" — 3.5 is Flash) |
| `gemini-3.5-flash` | Google | AI Pro bundle | (Pro bundle) | 1M | Paid | newest Flash, agentic/coding-tuned |
| `gemini-2.5-flash` | Google | Free API tier (GFP pool) | `gfp-pool` | 1M | Free | ~10 RPM · ~250k TPM · ~1,500 RPD **per project** |
| `gemini-2.0-flash` | Google | Free API tier (GFP pool) | `gfp-pool` | 1M | Free | ~15 RPM · 1M TPM · ~1,500 RPD **per project** |
| `gemini-2.0-flash-lite` | Google | Free API tier | `gfp-pool` | 1M | Free | lighter, higher RPM |
| `glm-5.2` | Zhipu / z.ai | z.ai GLM plan **or** OpenCode Go | `opencode-acc1` | 1.05M / 131k out | Paid | see "Subs worth adding" |

---

## Key facts

- **Gemini free-tier quota is per-Google-Cloud-PROJECT, not per-API-key.** Multiple keys
  in one project share one RPM/RPD/TPM pool. The GFP pool therefore uses **one key per
  project** across all Gemini-enabled projects (7 projects as of 2026-07-06:
  nself-gemini-1/2/3, nclaw-ali-4/5, gen-lang-client-*, openclaw-io). More capacity =
  more projects, not more keys. Google cut free-tier limits 50–80% in Dec 2025.
- **Gemini Pro** routes through the owner's paid Google AI Pro (Google One) subscription
  via the agy / `cloudcode-pa` path using `~/.cascade/agy-token.json`. Owner-authorized
  for personal use. Residual risk: Google's abuse detection may flag non-official-client
  access even for the account owner — monitor.
- **OpenCode's Gemini access** (if configured) comes from a third-party OAuth plugin
  (`opencode-gemini-auth`) against the Google AI Pro sub — **not** from OpenCode Go
  (which bundles Chinese open models only). Same ToS class as the agy path.

---

## Subs worth adding

| Option | Cost | Verdict |
|---|---|---|
| **OpenCode Go** | ~$10/mo | **Add** — cheapest path to GLM-5.2 + DeepSeek V4 + Qwen3.7 + Kimi K2.7 + MiniMax in one sub |
| z.ai GLM Coding Plan (Lite) | ~$18/mo | Skip unless GLM-5.2 needed at high volume (OpenCode Go's GLM quota is shared across its roster) |
| DeepSeek / Qwen / MiniMax standalone | API | Skip — already reachable via OpenCode Go |
| Mistral (Codestral) | — | Skip — not competitive for Cascade's use case |

---

## Sources

Anthropic: [Sonnet 5](https://www.anthropic.com/news/claude-sonnet-5) ·
[Fable 5 / Mythos 5](https://www.anthropic.com/news/claude-fable-5-mythos-5) ·
[Models overview](https://platform.claude.com/docs/en/about-claude/models/overview).
OpenAI: [GPT-5.5](https://openai.com/index/introducing-gpt-5-5/) ·
[Codex models](https://developers.openai.com/codex/models).
Google: [Gemini subscriptions](https://gemini.google/subscriptions/) ·
[Gemini 3.1 Pro](https://deepmind.google/models/gemini/pro/) ·
[API rate limits](https://ai.google.dev/gemini-api/docs/rate-limits).
z.ai: [GLM Coding Plan](https://z.ai/subscribe) · [GLM 5.2](https://openrouter.ai/z-ai/glm-5.2).
OpenCode: [Go docs](https://opencode.ai/docs/go/) ·
[gemini-auth plugin](https://github.com/jenslys/opencode-gemini-auth).
