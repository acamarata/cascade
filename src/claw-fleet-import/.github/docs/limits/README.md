# Provider usage limits — reference

Reference docs for the four AI providers claw-fleet tracks (or wants to track).
Each provider doc covers subscription tiers, per-tier message/token/request
quotas, reset windows, what claw-fleet can probe, and the gotchas a monitor
builder runs into.

| Provider | File | Status in claw-fleet |
|---|---|---|
| Claude (Anthropic) | [claude.md](claude.md) | Live — `/api/oauth/usage` |
| Codex (OpenAI ChatGPT) | [codex.md](codex.md) | Live — `x-codex-*` headers |
| Gemini (Google) | [gemini.md](gemini.md) | Live — API key (Mode A) or ADC Cloud Monitoring (Mode B) |
| OpenCode Go | [opencode.md](opencode.md) | Live — HTML scrape with session cookie; replace with REST API when [opencode PR #16513](https://github.com/anomalyco/opencode/pull/16513) ships |

Last reviewed: 2026-05-15.

---

## Monitorability matrix

What each provider exposes to a polling monitor like claw-fleet, today:

| Provider surface | Endpoint | Auth | What it returns | Refresh budget | Failure mode |
|---|---|---|---|---|---|
| Claude Max / Pro | `GET /api/oauth/usage` | Bearer (OAuth, `anthropic-beta: oauth-2025-04-20`) | `five_hour`, `seven_day`, `seven_day_sonnet`, `seven_day_opus` — each with `utilization` 0-100 + `resets_at` | ~1 probe/min/account before throttling | 200 OK with `{"error":{"type":"rate_limit_error"}}` envelope; back off |
| Claude API | `x-anthropic-ratelimit-*` headers on any `/v1/messages` call | API key | RPM/ITPM/OTPM remaining + reset | Free with each real request | Standard 429 |
| Codex (ChatGPT) | `POST /backend-api/codex/responses` (probe) | ChatGPT OAuth access token | `x-codex-{primary,secondary}-{used-percent,reset-at,window-minutes}`, `x-codex-plan-type`, `x-codex-active-limit`, `x-codex-credits-*` | ~1 probe/5min/account; 429 responses still carry headers | 401 = expired, 5xx = transient |
| Gemini consumer (AI Pro / Ultra) | none | n/a | n/a — no public usage API for consumer plans | n/a | Cookie session probe verifies *logged in* only; quota numbers not available |
| Gemini API / G1 Mode A | Gemini API endpoint | API key (`GEMINI_API_KEY_OPENCLAW`) | Model quota metrics per project | Generous | Requires a paid API key separate from AI Pro consumer subscription |
| Gemini API / G1 Mode B | Cloud Monitoring `timeSeries:query` on `serviceruntime.googleapis.com/quota/rate/net_usage` filtered to `generativelanguage.googleapis.com` | ADC / service account | Quota consumption metrics per project per minute | GCP Monitoring quotas (generous) | Requires `gcloud auth application-default login` once; more accurate than Mode A |
| Vertex AI | Cloud Monitoring on `aiplatform.googleapis.com` | ADC | Per-region quota metrics | GCP Monitoring | Not currently probed by claw-fleet |
| OpenCode Go (O1) | `opencode.ai/workspace/<id>/go` (HTML scrape) | Session cookie (`OPENCODE_GO_AUTH_COOKIE`) | Per-window utilization % | ~1 probe/5min | Cookie expires ~30 days; [PR #16513](https://github.com/anomalyco/opencode/pull/16513) will replace this with a REST endpoint |

**Gemini and OpenCode are now live.** The original "two of four have no endpoint" note no longer applies. All four providers are monitorable, though with caveats: Gemini consumer quotas remain opaque (Mode A/B only work for Gemini API keys, not the AI Pro consumer plan), and the OpenCode scrape will break if their page structure changes before the REST API ships.

---

## Subscription comparison

Flat table of the consumer-and-developer plans most relevant to a heavy AI-coding user. Prices are USD/month unless noted.

| Plan | Price | Surface | 5h cap | Weekly cap | API access? | Monitor? |
|---|---|---|---|---|---|---|
| **Claude Free** | $0 | claude.ai | small (single-digit msgs) | none | no | no |
| **Claude Pro** | $20 | claude.ai + Claude Code | ~45 Sonnet 4 msgs / 5h | 60-90 Sonnet hrs/week (May 2026 promo: 90) | no | yes via OAuth |
| **Claude Max 5×** | $100 | claude.ai + Claude Code | 5× Pro | 5× Pro | no | yes |
| **Claude Max 20×** | $200 | claude.ai + Claude Code | 20× Pro | 20× Pro (includes Opus quota) | no | yes |
| **Claude API Tier 1** | $5 credit | api.anthropic.com | RPM 50, ITPM 50K | no weekly | yes | yes (headers) |
| **ChatGPT Free** | $0 | chat.openai.com | minimal | none | no | no |
| **ChatGPT Go** | $8 | chat + Codex (trial) | trial only | trial only | no | partial |
| **ChatGPT Plus** | $20 | chat + Codex | baseline 5h cap | weekly cap | no | yes via OAuth |
| **ChatGPT Pro 5×** | $100 | chat + Codex | 5× Plus (10× through 2026-05-31) | 5× Plus | no | yes |
| **ChatGPT Pro 20×** | $200 | chat + Codex | 20× Plus (25× through 2026-05-31) | 20× Plus | no | yes |
| **ChatGPT Business / Enterprise** | varies | chat + Codex | unlimited / credits | unlimited / credits | yes (via API key) | yes |
| **Google AI Pro** | $19.99 | gemini.google.com only | per the credit-pool system (see gemini.md) | unpublished | **no** | session-alive only |
| **Google AI Ultra** | $249.99 | gemini.google.com only | 4-20× Pro (relative) | unpublished | **no** | session-alive only |
| **Gemini API free** | $0 | ai.google.dev (separate from consumer) | RPM/RPD per model | RPD per model | yes | yes via Cloud Monitoring |
| **Gemini API paid (Tier 1-3)** | pay-as-you-go | ai.google.dev | RPM/TPM scales with tier | none | yes | yes |
| **OpenCode free** | $0 | opencode TUI + free models (Zen pay-as-you-go for everything else) | rate-limited per model | none | yes (BYO keys) | no built-in |
| **OpenCode Go** | $19 | opencode TUI + hosted open-weight models | per-model 5h table (see opencode.md) | per-model weekly table | yes (via Zen / addons) | no public API yet |

Notes:
- "API access" means: does the subscription itself grant access to the provider's developer API. **Buying AI Pro does NOT give you Gemini API access. Buying ChatGPT Plus does NOT give you OpenAI API access.** These are separate billing surfaces on the same account.
- Anthropic Max plans are the exception — Claude Code runs on the consumer Max subscription via OAuth without a separate API tier.
- OpenCode addon model: ChatGPT and GitHub Copilot supported via OAuth passthrough; Anthropic OAuth passthrough was killed by Anthropic on 2026-01-09 and now requires an API key, Zen balance, or a third-party proxy.

---

## Reset windows summary

Sliding vs fixed, and how they actually drain:

| Window | Claude | Codex | Gemini consumer | Gemini API | OpenCode Go |
|---|---|---|---|---|---|
| 5-hour | Rolling, per-account, fixed 5h sliding window | Rolling 5h, fixed window | Rolling 5h (post-May 2026 credit-pool model) | n/a | Rolling 5h, per-model |
| Daily | n/a | n/a | Was 100 msg/day pre-May 2026; now folded into the credit pool | RPD per model, midnight Pacific | n/a |
| Weekly | Rolling 7d, started Aug 2025, separate Sonnet + Opus sub-buckets that drain simultaneously with the parent | Rolling 7d, sliding window | Unpublished weekly cap (new since I/O 2026) | n/a | Rolling 7d, per-model |
| Monthly | n/a | n/a | n/a | n/a | Rolling 30d, per-model |

The single most important quirk: **on Claude, `seven_day_sonnet` and `seven_day_opus` are NOT independent budgets that add on top of `seven_day`.** They are sub-counters of the same pool. Exhausting `seven_day` blocks Sonnet even if Sonnet has quota remaining. This bites people regularly ([anthropics/claude-code#57875](https://github.com/anthropics/claude-code/issues/57875)).

---

## What claw-fleet does today vs what it can't

**Today (verified live):**
- Claude Max: 5h + weekly + per-model windows via OAuth usage endpoint (every 60s, with rate-limit-error back-off 2 → 10min).
- Codex: 5h primary + weekly secondary windows via probe-and-read-headers (every 5min). 429-fallback header capture works for capped accounts.
- Gemini API (G1): Mode A uses `GEMINI_API_KEY_OPENCLAW` to probe the Gemini API for quota metrics (every 5min). Mode B upgrades to Cloud Monitoring `timeSeries:query` when ADC is configured (`gcloud auth application-default login`). Mode B is more accurate but requires a one-time setup step.
- OpenCode Go (O1): HTML scrape of the workspace usage page using a session cookie (every 5min). Reports per-model 5h and weekly utilization percentages. Cookie expires ~30 days; re-export from Chrome when `auth_expired` appears.
- Hourly sanity check (`io.clawfleet.sanity`, every 1h): validates resets are in the future and within sane bounds, surfaces anomalies into the `sanity` block of the cache.

**Not feasible without provider changes:**
- Gemini consumer quota (AI Pro / Ultra) — no public surface; the in-app counter was removed post-I/O 2026.
- OpenCode Go REST API — the scrape works today, but [PR #16513](https://github.com/anomalyco/opencode/pull/16513) will replace it with a proper endpoint. The scrape will break if their page structure changes first.

**Plausible future work:**
- Helicone sidecar for OpenCode — every Go request gets a Helicone observability header; query Helicone's API for aggregate cost. Higher effort, gives token-level resolution instead of request-count.
- Additional Gemini API projects — currently only `openclaw-io` (default). Users with multiple GCP projects could surface a G2, G3 row by setting additional project ID env vars.

---

## Doc maintenance

These limits change constantly. The Gemini consumer plan changed twice in 2026 alone (100 → 25 messages/day, then dropped entirely for a credit pool). The Codex 5×/20× Pro tiers got a one-month 10×/25× promotion that expires 2026-05-31. Set a quarterly re-verify reminder, and update each provider's `Last updated` line in its file when the numbers move.

Run `~/bin/claw-fleet-sanity` to surface any cache anomalies that suggest a limit shape has changed (e.g., a reset timestamp falling outside the documented window length).
