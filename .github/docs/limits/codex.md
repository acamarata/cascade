# Codex (OpenAI ChatGPT) — Usage Limits

## What is Codex

OpenAI Codex CLI (`codex` command) is an agentic coding assistant that runs in the terminal, IDE extensions, and as a cloud task runner. It was introduced on **April 16, 2025** alongside o3 and o4-mini, moved into research preview for Codex Cloud on May 16, 2025, and reached **general availability on October 6, 2025**. Desktop apps for macOS and Windows launched February 2 and March 4, 2026 respectively. As of April 2026, it had ~3 million weekly active users.

Codex is not a standalone product with its own billing. It is bundled into ChatGPT subscription plans and authenticated via your ChatGPT account OAuth token, not a Platform API key. The two paths are meaningfully different: ChatGPT-plan access goes through `chatgpt.com/backend-api/codex/responses`; API-key access goes through `api.openai.com/v1/responses` and is billed per-token.

Which plans include Codex CLI:

| Plan | Codex CLI included |
|------|--------------------|
| Free | Limited trial access (Codex Mini only, temporary promotion as of 2026-05) |
| Go ($8/mo) | Limited trial access (temporary promotion as of 2026-05) |
| Plus ($20/mo) | Yes — full Codex CLI, IDE, and cloud tasks |
| Pro ($100 or $200/mo) | Yes — higher multiplier (5x or 20x vs. Plus) |
| Business (~$25–30/seat/mo) | Yes — same base limit as Plus |
| Enterprise | Yes — credits-based, no fixed rate limits |
| Edu | Yes — same credits model as Enterprise |

Sources: [Using Codex with your ChatGPT plan](https://help.openai.com/en/articles/11369540-using-codex-with-your-chatgpt-plan) · [Codex pricing page](https://developers.openai.com/codex/pricing) · [TechCrunch launch coverage](https://techcrunch.com/2025/04/16/openai-debuts-codex-cli-an-open-source-coding-tool-for-terminals/)

---

## Subscription tiers

| Plan | Price (USD/mo) | Codex CLI | Other AI surfaces | Notes |
|------|---------------|-----------|-------------------|-------|
| **Free** | $0 | Trial/Mini only | GPT-5.3 (10 msg/5h) | Limited promo as of 2026-05; expected to revert |
| **Go** | $8 | Trial/Mini only | GPT-5.3 Instant, files, images | Trial promo as of 2026-05 |
| **Plus** | $20 | Yes — baseline tier | Full model suite, Deep Research (10/mo), Sora, Agent Mode | The minimum plan for reliable Codex CLI use |
| **Pro 5x** | $100 | Yes — 5× Plus limits | All Plus surfaces | 10× effective through 2026-05-31 (promotional double) |
| **Pro 20x** | $200 | Yes — 20× Plus limits | All Plus surfaces | 25× effective through 2026-05-31 (promotional double) |
| **Business** | ~$25–30/seat | Yes — same base as Plus | Workspace controls, admin panel | Credits-enabled; billed monthly or annually |
| **Enterprise** | Custom | Yes — credits only | Custom limits, SSO, DLP | No fixed rate limits; scales with credit balance |

Sources: [ChatGPT pricing overview](https://chatgpt.com/pricing/) · [Fritz AI pricing breakdown](https://fritz.ai/chatgpt-pricing/) · [Codex pricing developer docs](https://developers.openai.com/codex/pricing)

---

## Per-tier message / request limits

Limits operate on two independent rolling windows. Both use a credit-based accounting model (changed from per-message to token-derived on April 2, 2026).

### Primary window (5-hour rolling)

Measured in "credits" consumed per request. Credit cost scales with model complexity and actual token usage. Ranges below are approximate — the exact credit draw varies by task length. Numbers reflect the **post-April-9, 2026 accounting** and the **pre-May-31 promotional multiplier** where noted.

| Plan | Model | Local messages (soft→hard) | Cloud tasks (soft→hard) | Code reviews (soft→hard) |
|------|-------|---------------------------|------------------------|--------------------------|
| **Plus / Business** | GPT-5.5 | 15–80 | — | — |
| **Plus / Business** | GPT-5.4 | 20–100 | — | — |
| **Plus / Business** | GPT-5.4-mini | 60–350 | — | — |
| **Plus / Business** | GPT-5.3-Codex | 30–150 | 10–60 | 20–50 |
| **Pro 5x** | GPT-5.5 | 80–400 | — | — |
| **Pro 5x** | GPT-5.4 | 100–500 | — | — |
| **Pro 5x** | GPT-5.3-Codex | 150–750 | 50–300 | 100–250 |
| **Pro 20x** | GPT-5.5 | 300–1,600 | — | — |
| **Pro 20x** | GPT-5.4 | 400–2,000 | — | — |
| **Pro 20x** | GPT-5.3-Codex | 600–3,000 | 200–1,200 | 400–1,000 |
| **Enterprise** | All | No fixed cap | No fixed cap | No fixed cap |

"Soft cap" = throughput slows (soft rate limit). "Hard cap" = requests rejected (429) until window resets. Promotional doubles apply through 2026-05-31 for Pro plans.

### Secondary window (7-day rolling)

Window is **10,080 minutes** (7 days). It is a rolling window anchored to your first request in the window, not a fixed Monday-midnight calendar reset. OpenAI has also issued unscheduled resets (e.g., April 28, 2026 — a manual reset across all paid plans to mark a product milestone). These are unpredictable; do not rely on them.

Specific numeric caps for the secondary window are **not publicly documented** by OpenAI. Community reports suggest the secondary cap is roughly 4–8× the primary cap over the 7-day window, but this has not been confirmed officially.

### Model accounting and `x-codex-active-limit`

Each model draws a different credit amount per request. Approximate credit-to-message rates (as of 2026-05):

| Model | Credits per local message | Credits per cloud task |
|-------|--------------------------|------------------------|
| GPT-5.5 | ~14 | n/a |
| GPT-5.4 | ~7 | n/a |
| GPT-5.3-Codex | ~5 | ~25 |
| GPT-5.4-mini | ~2 | n/a |

`x-codex-active-limit` reflects the current active limiting context. Observed values include `"plus"` and `"premium"` — the full enumeration is **not officially documented**. Based on observed behavior, `"premium"` appears for Pro plan tokens; `"plus"` for Plus/Business. Enterprise accounts may return a different value or omit the header.

Sources: [Codex rate card (OpenAI Help)](https://help.openai.com/en/articles/20001106-codex-rate-card) · [LaoZhang usage limits analysis](https://blog.laozhang.ai/en/posts/openai-codex-usage-limits) · [Community rate limits discussion](https://community.openai.com/t/codex-rate-limits-discussion-thread/1378553) · [April 9 limit system analysis](https://community.openai.com/t/understanding-the-new-codex-limit-system-after-the-april-9-update/1378768)

---

## Reset windows

### Primary (5-hour rolling)

The 5-hour window is a **sliding window** anchored to the first request that started consuming quota in a given cycle. It is not tied to wall-clock hours (not reset at 00:00, 06:00, etc.). When `x-codex-primary-reset-at` (epoch seconds) is in the past or near, a new 5-hour bucket opens.

### Secondary (7-day rolling)

`x-codex-secondary-window-minutes` consistently returns `10080` (7 × 1440). The window is rolling, anchored similarly to the first consumption event in the window. There is a known inconsistency (GitHub issue [#23190](https://github.com/openai/codex/issues/23190)) where the reset timestamp can appear to jump — this is a bucket-switching behavior on OpenAI's backend, not a client-side parsing error.

### Unscheduled resets

OpenAI has issued out-of-band resets (April 28, 2026; February 2026). These are not predictable. The `x-codex-*-reset-at` headers reflect the *current* window expiry, not a promise of future resets.

### Alignment with Anthropic reset events

No evidence that OpenAI aligns its Codex limit resets with Anthropic's schedule. They are independent systems.

Sources: [Reset history analysis](https://www.knightli.com/en/2026/05/17/codex-usage-limit-reset-history/) · [April 28 reset discussion](https://community.openai.com/t/codex-rate-limits-reset-for-all-paid-plans-april-28-2026/1379921) · [Weekly meter inconsistency issue](https://github.com/openai/codex/issues/23190)

---

## Monitoring — what claw-fleet can extract

### The probe endpoint

`POST https://chatgpt.com/backend-api/codex/responses`

This is the same endpoint the Codex CLI itself uses for every request. A minimal probe that retrieves headers without consuming significant quota:

```json
{
  "model": "gpt-5.4",
  "instructions": "x",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "x"}]
    }
  ],
  "reasoning": {"effort": "none"},
  "stream": true,
  "store": false
}
```

**Why `stream: true, store: false`:** The endpoint requires `stream: true` for OAuth (ChatGPT account) auth — non-stream requests fail with a 400 validation error. `store: false` prevents persisting the conversation. Closing the SSE stream immediately after receiving the first event (e.g., `response.created`) minimizes token consumption. Malformed or rejected requests do not consume quota.

**Required request headers:**

```
Authorization: Bearer <oauth_access_token>
Content-Type: application/json
User-Agent: codex_cli_rs/0.100.0
Originator: codex_cli_rs
OpenAI-Beta: responses=v1
```

`User-Agent` and `Originator` identify the request as coming from the Codex CLI (not the web UI). Without them, the endpoint may behave differently or reject the request.

### Response headers — complete table

| Header | Type | Meaning | Example |
|--------|------|---------|---------|
| `x-codex-plan-type` | string | Subscription tier of the authenticated account | `"plus"`, `"premium"` |
| `x-codex-active-limit` | string | Active limit context name | `"plus"`, `"premium"` |
| `x-codex-primary-used-percent` | float (0–100) | Percent of the 5h primary window consumed | `40.0` |
| `x-codex-primary-reset-at` | int (Unix epoch, seconds) | When the current 5h window resets | `1779571027` |
| `x-codex-primary-window-minutes` | int | Length of the primary window in minutes | `300` |
| `x-codex-secondary-used-percent` | float (0–100) | Percent of the 7-day window consumed | `21.0` |
| `x-codex-secondary-reset-at` | int (Unix epoch, seconds) | When the current 7-day window resets | `1779144603` |
| `x-codex-secondary-window-minutes` | int | Length of the secondary window in minutes | `10080` |
| `x-codex-credits-has-credits` | bool-like string | Whether the account has purchased extra credits | `"true"`, `"false"` |
| `x-codex-credits-balance` | numeric string | Credit balance (only present if `credits-has-credits` is true) | `"250.0"` |
| `x-codex-credits-unlimited` | bool-like string | Whether credits are unlimited (Enterprise) | `"false"` |

Notes:
- `x-codex-primary-reset-at` and `x-codex-secondary-reset-at` return the epoch as an integer (not milliseconds, not ISO 8601).
- `x-codex-credits-has-credits` being `"true"` indicates the user has purchased additional Codex credits on top of their plan allocation.
- `x-codex-credits-unlimited` appears to be `"true"` for Enterprise accounts.
- Header names observed in community tooling ([hermes-agent issue #9085](https://github.com/NousResearch/hermes-agent/issues/9085)). The full set is not formally documented by OpenAI.

### 429 responses still carry headers

When the primary or secondary window is exhausted, OpenAI returns HTTP 429 with the same `x-codex-*` header family. This means a capped account can still be polled: the 429 body conveys no new info, but the headers in the 429 response carry the exact same usage percentages and reset timestamps as a 200. claw-fleet reads these headers regardless of HTTP status.

### OAuth auth flow

1. User runs `codex login` — opens a browser to `chatgpt.com` OAuth flow.
2. After consent, the browser returns an access token to the CLI via a localhost callback.
3. Token is cached at `~/.codex/auth.json` (plaintext; treat like a password).
4. The Codex CLI refreshes tokens automatically during active sessions. Expiry duration is not publicly documented, but inactive sessions require re-login.
5. For headless/CI use: copy `auth.json` to the target machine, or use device-code auth (beta), or port-forward the localhost OAuth callback over SSH.

claw-fleet reads `~/.codex/auth.json` for the `access_token` field and uses it as the `Authorization: Bearer` value.

### OpenAI developer Usage API — separate system

The [OpenAI Platform Usage API](https://platform.openai.com/usage) (`api.openai.com/v1/usage`) tracks **API-key** consumption billed to the Platform account. It does **not** reflect ChatGPT subscription Codex usage. Consumer ChatGPT accounts (Plus, Pro, Business) are billed differently and do not appear in the Platform dashboard. The two systems are entirely separate.

Sources: [Codex auth docs](https://developers.openai.com/codex/auth) · [CI/CD auth guide](https://developers.openai.com/codex/auth/ci-cd-auth) · [hermes-agent issue surfacing headers](https://github.com/NousResearch/hermes-agent/issues/9085) · [openclaw store:false bug](https://github.com/openclaw/openclaw/issues/67740)

---

## Known gotchas

### Token refresh

Tokens refresh automatically during active sessions. If `auth.json` is older than the session expiry (duration unknown, empirically seems to be days to weeks), the next request returns 401. claw-fleet must detect 401 and surface a re-login prompt — it cannot refresh the token programmatically without user interaction (no refresh token is exposed in `auth.json`).

`auth.json` structure (observed):

```json
{
  "access_token": "eyJ...",
  "account_id": "user-...",
  "expires_at": 1779999999
}
```

`expires_at` is a Unix epoch. When `Date.now()/1000 > expires_at`, the next probe will 401.

### The 5-hour reset inconsistency

The 5-hour window is sliding, not wall-clock. Multiple users report their `x-codex-primary-reset-at` shifts unexpectedly between calls — this is the same backend bucket-switching behavior documented in the 7-day window. If your monitor sees the reset timestamp move backwards, treat it as a new window opening, not a clock error.

### Minimal probe quota cost

The `"reasoning": {"effort": "none"}` + immediate stream close pattern burns negligible quota (a few tokens for the prompt echo before the stream is aborted). However, sending probes more frequently than every 60 seconds is inadvisable — it is not free, and there is no documented rate limit for the probe pattern itself. If you hit a 429 on the probe itself (not the Codex limit), back off with exponential jitter.

### 401 vs 429 vs 5xx

| Status | Meaning | Action |
|--------|---------|--------|
| 200 | OK — headers are fresh | Read x-codex-* headers |
| 400 | Bad request — malformed probe body | Fix the probe; do not retry |
| 401 | Token expired or invalid | Re-login required; surface to user |
| 429 | Rate limited — primary or secondary window full | Read x-codex-* headers anyway (they are present); wait until reset-at |
| 5xx | OpenAI backend error | Retry with exponential backoff; do not read headers as authoritative |

### `x-codex-credits-has-credits`

This header is `"true"` when the user has **purchased additional credits** beyond their plan's included allocation. It does not indicate that plan-included quota is still available. A user can have `credits-has-credits: "true"` and still be at 100% on `primary-used-percent` if the purchased credits are allocated to a separate pool that Codex draws from after the plan cap is hit. The exact interaction between purchased credits and the primary/secondary windows is **not officially documented**.

### `stream: true` requirement

Sending `stream: false` with an OAuth token returns a 400 with a message along the lines of "store and stream mismatch." Only streaming is supported for ChatGPT-account Codex requests. This is different from the Platform API where both modes work.

### No overlap with Platform Usage API

Querying `api.openai.com/v1/usage` with a Platform API key will not show ChatGPT subscription consumption. Do not attempt to cross-reference them — they are independent billing systems.

Sources: [Codex auth docs](https://developers.openai.com/codex/auth) · [openclaw store:false mismatch bug](https://github.com/openclaw/openclaw/issues/67740) · [credits-limit reached before credits run out](https://community.openai.com/t/codex-shows-a-limit-reached-message-before-credits-run-out/1380140) · [no warning before rate limit shutoff](https://github.com/openai/codex/issues/2903)

---

## Sources

- [Introducing Codex — OpenAI](https://openai.com/index/introducing-codex/)
- [Codex now generally available — OpenAI](https://openai.com/index/codex-now-generally-available/)
- [Codex Changelog — OpenAI Developers](https://developers.openai.com/codex/changelog)
- [Codex Models — OpenAI Developers](https://developers.openai.com/codex/models)
- [Codex Pricing — OpenAI Developers](https://developers.openai.com/codex/pricing)
- [Codex Rate Card — OpenAI Help Center](https://help.openai.com/en/articles/20001106-codex-rate-card)
- [Using Codex with your ChatGPT plan — OpenAI Help Center](https://help.openai.com/en/articles/11369540-using-codex-with-your-chatgpt-plan)
- [Codex Authentication — OpenAI Developers](https://developers.openai.com/codex/auth)
- [Codex CI/CD Auth — OpenAI Developers](https://developers.openai.com/codex/auth/ci-cd-auth)
- [ChatGPT Pricing — chatgpt.com](https://chatgpt.com/pricing/)
- [April 9 limit system — OpenAI Community](https://community.openai.com/t/understanding-the-new-codex-limit-system-after-the-april-9-update/1378768)
- [Rate limits discussion — OpenAI Community](https://community.openai.com/t/codex-rate-limits-discussion-thread/1378553)
- [April 28 reset event — OpenAI Community](https://community.openai.com/t/codex-rate-limits-reset-for-all-paid-plans-april-28-2026/1379921)
- [Weekly meter inconsistency — openai/codex #23190](https://github.com/openai/codex/issues/23190)
- [x-codex header feature request — hermes-agent #9085](https://github.com/NousResearch/hermes-agent/issues/9085)
- [store:false mismatch — openclaw #67740](https://github.com/openclaw/openclaw/issues/67740)
- [Credits limit vs credits balance — OpenAI Community](https://community.openai.com/t/codex-shows-a-limit-reached-message-before-credits-run-out/1380140)
- [LaoZhang usage limits deep-dive](https://blog.laozhang.ai/en/posts/openai-codex-usage-limits)
- [TechCrunch Codex CLI launch](https://techcrunch.com/2025/04/16/openai-debuts-codex-cli-an-open-source-coding-tool-for-terminals/)
- [Reset history analysis](https://www.knightli.com/en/2026/05/17/codex-usage-limit-reset-history/)

---

## Last updated

2026-05-15
