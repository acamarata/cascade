# Claude (Anthropic) — Usage Limits

## Subscription tiers

Consumer and team plans use the claude.ai surface; API plans use platform.claude.com. Claude Code runs on the OAuth surface (using your claude.ai subscription, not an API key) — claw-fleet monitors the OAuth usage endpoint.

| Plan | Price (USD) | Surface | Claude Code Max | Notes |
|---|---|---|---|---|
| Free | $0/month | claude.ai (web, iOS, Android, desktop) | No | Rate-limited; daily caps not published. No API access. |
| Pro | $17/mo annual / $20/mo monthly | claude.ai + Claude Code | No | ~5× Free usage per 5h window. All models. |
| Max (5×) | $100/month | claude.ai + Claude Code | **Yes** | 5× Pro usage. Separate weekly Sonnet + Opus budgets. Extra usage add-on available. |
| Max (20×) | $200/month | claude.ai + Claude Code | **Yes** | 20× Pro usage. Larger weekly budgets. Extra usage add-on available. |
| Team Standard | $20/mo annual / $25/mo monthly per seat | claude.ai + Claude Code | No | Comparable to Pro per seat. Org billing. |
| Team Premium | $100/mo annual / $125/mo monthly per seat | claude.ai + Claude Code | **Yes** | 5× Standard seat. Matches Max 5× usage. |
| Enterprise | Custom (reported ~$60+/seat, 70-seat floor) | claude.ai + API + Claude Code | Yes | Usage-based API billing instead of usage limits. SCIM, audit logs, HIPAA-ready. |
| API Tier 1 | Pay-as-you-go (≥$5 credit) | API only | N/A (API key, not OAuth) | $500/month spend cap. |
| API Tier 2 | Pay-as-you-go (≥$40 cumulative) | API only | N/A | $500/month spend cap. |
| API Tier 3 | Pay-as-you-go (≥$200 cumulative) | API only | N/A | $1,000/month spend cap. |
| API Tier 4 | Pay-as-you-go (≥$400 cumulative) | API only | N/A | $200,000/month spend cap. |
| Monthly Invoicing | Custom (via sales) | API only | N/A | No monthly spend cap. Net-30 terms. |

Sources: [claude.com/pricing](https://claude.com/pricing), [TechCrunch weekly limits announcement](https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/), [finout.io 2026 overview](https://www.finout.io/blog/claude-pricing-in-2026-for-individuals-organizations-and-developers)

---

## Per-tier rate and usage limits

### Claude.ai + Claude Code subscription plans (OAuth surface)

These are the limits that `five_hour`, `seven_day`, `seven_day_sonnet`, and `seven_day_opus` track.

Anthropic does not publish exact token counts for subscription plans. Limits are expressed as relative multipliers and approximate "hours of use." All numbers below are from the [July 2025 announcement](https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/) with a 50% increase applied through July 13, 2026 (per a [May 13, 2026 promotion](https://pasqualepillitteri.it/en/news/2614/claude-resets-rate-limits-5-hour-weekly-may-15-2026)).

#### 5-hour rolling window

Applies to all paid plans. Tracks cross-model usage within any rolling 5-hour period. Hitting 100% blocks new requests until the window slides forward.

| Plan | Approx. messages per 5h window (observed) |
|---|---|
| Free | Not published; daily soft cap, lower than Pro |
| Pro | ~150–225 (varies by model and message length) |
| Max 5× | ~750–900 |
| Max 20× | ~3,000–3,600 |

The message-count estimates are community-observed and vary heavily with model choice and output length. Anthropic does not publish a fixed number. [Source: Claude Help Center](https://support.claude.com/en/articles/9797557-usage-limit-best-practices)

#### 7-day weekly windows (added August 28, 2025)

Two independent weekly budgets exist for Max and Pro plans. Sonnet usage drains **both** the overall weekly bucket and the Sonnet-specific bucket simultaneously — a known implementation issue as of early 2026 that makes the per-model split less independent than intended.

| Plan | Weekly all-models (Sonnet 4 hours, approx.) | Weekly Opus 4 (hours, approx.) |
|---|---|---|
| Pro | 60–90 | No separate Opus budget |
| Max 5× | 210–420 | 15–35 |
| Max 20× | 360–720 | 24–40 |

> **Note:** These figures reflect the May 2026 +50% temporary increase. Pre-promotion baselines: Pro 40–60h, Max 5× 140–280h / 15–35h Opus, Max 20× 240–480h / 24–40h Opus. The promotion runs through July 13, 2026, after which limits revert. [Source: frozenlight.ai](https://www.frozenlight.ai/post/frozenlight/710/anthropic-sets-weekly-usage-limits-measured-in-hours/)

The weekly limit affects fewer than 5% of subscribers based on Anthropic's stated estimate. Extra usage credits can be purchased to continue past the weekly cap at standard API rates.

---

### API tier limits (API key surface — not monitored by the OAuth endpoint)

Token rate limits use a **token bucket algorithm** — capacity replenishes continuously, not at fixed intervals. RPM limits may burst down to 1 req/sec at lower tiers.

**Important:** For most models, `cache_read_input_tokens` do NOT count toward ITPM. Only `input_tokens` (post-cache) + `cache_creation_input_tokens` count. Claude Haiku 3.5 is the exception (†).

Source: [platform.claude.com/docs/en/api/rate-limits](https://platform.claude.com/docs/en/api/rate-limits)

#### Messages API — Tier 1 (cumulative spend ≥$5)

| Model | RPM | ITPM | OTPM |
|---|---|---|---|
| Claude Sonnet 4.x (combined: 4.6, 4.5, 4.0) | 50 | 30,000 | 8,000 |
| Claude Haiku 4.5 | 50 | 50,000 | 10,000 |
| Claude Haiku 3.5 (retired†) | 50 | 50,000† | 10,000 |
| Claude Opus 4.x (combined: 4.7, 4.6, 4.5, 4.1, 4.0) | 50 | 500,000 | 80,000 |

#### Messages API — Tier 2 (cumulative spend ≥$40)

| Model | RPM | ITPM | OTPM |
|---|---|---|---|
| Claude Sonnet 4.x | 1,000 | 450,000 | 90,000 |
| Claude Haiku 4.5 | 1,000 | 450,000 | 90,000 |
| Claude Haiku 3.5 (retired†) | 1,000 | 100,000† | 20,000 |
| Claude Opus 4.x | 1,000 | 2,000,000 | 200,000 |

#### Messages API — Tier 3 (cumulative spend ≥$200)

| Model | RPM | ITPM | OTPM |
|---|---|---|---|
| Claude Sonnet 4.x | 2,000 | 800,000 | 160,000 |
| Claude Haiku 4.5 | 2,000 | 1,000,000 | 200,000 |
| Claude Haiku 3.5 (retired†) | 2,000 | 200,000† | 40,000 |
| Claude Opus 4.x | 2,000 | 5,000,000 | 400,000 |

#### Messages API — Tier 4 (cumulative spend ≥$400)

| Model | RPM | ITPM | OTPM |
|---|---|---|---|
| Claude Sonnet 4.x | 4,000 | 2,000,000 | 400,000 |
| Claude Haiku 4.5 | 4,000 | 4,000,000 | 800,000 |
| Claude Haiku 3.5 (retired†) | 4,000 | 400,000† | 80,000 |
| Claude Opus 4.x | 4,000 | 10,000,000 | 800,000 |

**Monthly spend limits by tier:**

| Tier | To enter (cumulative credit) | Monthly spend ceiling |
|---|---|---|
| Tier 1 | $5 | $500 |
| Tier 2 | $40 | $500 |
| Tier 3 | $200 | $1,000 |
| Tier 4 | $400 | $200,000 |
| Monthly Invoicing | Sales | None |

Tier advancement is automatic and immediate when the spend threshold is met.

#### Message Batches API limits (separate from Messages API, shared across models)

| Tier | RPM | Max queue depth | Max requests per batch |
|---|---|---|---|
| 1 | 50 | 100,000 | 100,000 |
| 2 | 1,000 | 200,000 | 100,000 |
| 3 | 2,000 | 300,000 | 100,000 |
| 4 | 4,000 | 500,000 | 100,000 |

---

## Reset windows

### 5-hour rolling window

- Starts counting from your first request in any session.
- Slides forward continuously — not anchored to a clock hour or day boundary.
- Example: usage spike at 10:00 → that usage falls off the window at 15:00, regardless of what else happened in between.
- When you hit 100% utilization, requests fail until the oldest usage rolls off.
- `five_hour.resets_at` in the OAuth usage API shows when the current high-water mark expires, not when the window resets to zero (the window never resets to zero; it slides).

### 7-day rolling window

- Also rolling, anchored to when heavy usage began, not a fixed weekday.
- `seven_day.resets_at` reflects the rolling expiry of the current usage peak.
- This means your reset day and time are personal and shift as you use the service.
- Users can check their specific reset time in **Settings > Usage** on claude.ai.
- There is a known display bug ([claude-code issue #51222](https://github.com/anthropics/claude-code/issues/51222)) where the displayed reset time can shift by days without an actual reset occurring — likely a timezone or rolling-window calculation bug in the UI.

### Per-model weekly windows (seven_day_sonnet, seven_day_opus)

- `seven_day_sonnet` and `seven_day_opus` track model-specific weekly budgets.
- Both share the same 7-day rolling clock as `seven_day`.
- In practice (as of early 2026), Sonnet usage drains both `seven_day` and `seven_day_sonnet` simultaneously. Exhausting `seven_day` blocks all access even if `seven_day_sonnet` still has headroom. This is a reported bug ([#57875](https://github.com/anthropics/claude-code/issues/57875)).

### One-time reset — May 15, 2026

Anthropic manually cleared both the 5-hour and 7-day counters for all active paid subscribers on May 15, 2026. This was a one-off operational decision to support weekend work and manage load. Not a recurring event. [Source: pasqualepillitteri.it](https://pasqualepillitteri.it/en/news/2614/claude-resets-rate-limits-5-hour-weekly-may-15-2026)

---

## Monitoring API — what claw-fleet can probe

### `/api/oauth/usage` endpoint

This is the primary endpoint claw-fleet uses. It is undocumented in Anthropic's official API docs but has been stable and actively used since at least late 2024.

**Request:**

```
GET https://api.anthropic.com/api/oauth/usage
Authorization: Bearer <access_token>
anthropic-beta: oauth-2025-04-20
Accept: application/json
Content-Type: application/json
```

The access token comes from `~/.claude/.credentials.json` (or macOS Keychain). It expires roughly every hour; Claude Code refreshes it proactively 5 minutes before expiry, or reactively on a 401/403 response.

**Response schema:**

```json
{
  "five_hour": {
    "utilization": 42.5,
    "resets_at": "2026-05-15T18:00:00Z"
  },
  "seven_day": {
    "utilization": 67.0,
    "resets_at": "2026-05-22T08:00:00Z"
  },
  "seven_day_sonnet": {
    "utilization": 55.0,
    "resets_at": "2026-05-22T08:00:00Z"
  },
  "seven_day_opus": {
    "utilization": 12.0,
    "resets_at": "2026-05-22T08:00:00Z"
  },
  "extra_usage": {
    "is_enabled": true,
    "used_credits": 1250,
    "monthly_limit": 0,
    "currency": "USD"
  }
}
```

**Field reference:**

| Field | Type | Description |
|---|---|---|
| `five_hour.utilization` | float 0–100 | % of 5-hour rolling budget consumed |
| `five_hour.resets_at` | ISO 8601 UTC | When the current usage peak rolls off the 5h window |
| `seven_day.utilization` | float 0–100 | % of all-models 7-day rolling budget consumed |
| `seven_day.resets_at` | ISO 8601 UTC | When the current usage peak rolls off the 7d window |
| `seven_day_sonnet.utilization` | float 0–100 | % of Sonnet-specific 7-day budget consumed (optional field) |
| `seven_day_sonnet.resets_at` | ISO 8601 UTC | Rolling expiry for Sonnet weekly budget |
| `seven_day_opus.utilization` | float 0–100 | % of Opus-specific 7-day budget consumed (optional; null on Pro) |
| `seven_day_opus.resets_at` | ISO 8601 UTC | Rolling expiry for Opus weekly budget |
| `extra_usage.is_enabled` | bool | Whether the user has enabled extra usage credits |
| `extra_usage.used_credits` | int (cents) | Credits consumed beyond plan in current billing month |
| `extra_usage.monthly_limit` | int (cents) | User-set monthly cap; 0 = unlimited |
| `extra_usage.currency` | string | Always "USD" in current implementation |

Optional fields (`seven_day_opus`, `seven_day_sonnet`, `extra_usage`) may be absent or null depending on plan. The openusage project also documents `seven_day_omelette` (separate Claude design-tool budget) as an optional field. Source: [openusage docs](https://github.com/robinebers/openusage/blob/main/docs/providers/claude.md)

**Rate limit on the usage endpoint itself:**

The endpoint rate-limits aggressively. As of mid-2025, even 30-second polling intervals triggered persistent 429 responses with no `Retry-After` header, requiring exponential backoff up to 300s+ with no recovery guarantee. This is tracked in [claude-code issue #31021](https://github.com/anthropics/claude-code/issues/31021) and [#31637](https://github.com/anthropics/claude-code/issues/31637). Both were closed as "not planned." Anthropic has not published a recommended polling interval.

**Practical recommendation:** Poll no faster than every 5 minutes. Back off exponentially on 429s. The data only changes meaningfully at the scale of hours, so aggressive polling adds no value.

**Error envelope (HTTP 429 or HTTP 200 with error body):**

```json
{
  "error": {
    "type": "rate_limit_error",
    "message": "Rate limited. Please try again later."
  }
}
```

Note: The endpoint can return an error envelope at HTTP 200 as well as HTTP 429 — check both the status code and the body.

---

### API messages response headers (for API-key users)

Every response from `/v1/messages` includes these rate-limit headers. Useful if claw-fleet monitors API-key accounts in addition to OAuth accounts.

| Header | Description |
|---|---|
| `retry-after` | Seconds to wait before retrying after a 429. |
| `anthropic-ratelimit-requests-limit` | Max requests per rate limit period. |
| `anthropic-ratelimit-requests-remaining` | Requests remaining in current period. |
| `anthropic-ratelimit-requests-reset` | RFC 3339 timestamp when request limit replenishes. |
| `anthropic-ratelimit-tokens-limit` | Max tokens (most restrictive active limit). |
| `anthropic-ratelimit-tokens-remaining` | Tokens remaining (rounded to nearest 1,000). |
| `anthropic-ratelimit-tokens-reset` | RFC 3339 timestamp when token limit replenishes. |
| `anthropic-ratelimit-input-tokens-limit` | Max input tokens per period. |
| `anthropic-ratelimit-input-tokens-remaining` | Input tokens remaining (nearest 1,000). |
| `anthropic-ratelimit-input-tokens-reset` | RFC 3339 timestamp. |
| `anthropic-ratelimit-output-tokens-limit` | Max output tokens per period. |
| `anthropic-ratelimit-output-tokens-remaining` | Output tokens remaining (nearest 1,000). |
| `anthropic-ratelimit-output-tokens-reset` | RFC 3339 timestamp. |
| `anthropic-priority-input-tokens-limit` | Priority Tier only — max priority input tokens. |
| `anthropic-priority-input-tokens-remaining` | Priority Tier only — priority input tokens remaining. |
| `anthropic-priority-input-tokens-reset` | Priority Tier only — RFC 3339 reset time. |
| `anthropic-priority-output-tokens-limit` | Priority Tier only. |
| `anthropic-priority-output-tokens-remaining` | Priority Tier only. |
| `anthropic-priority-output-tokens-reset` | Priority Tier only. |

The `anthropic-ratelimit-tokens-*` set always reflects the **most restrictive** active limit (workspace-level overrides org-level when tighter). Source: [platform.claude.com/docs/en/api/rate-limits](https://platform.claude.com/docs/en/api/rate-limits)

### Admin / Usage Report API (API-key accounts, Tier 3+)

The [Claude Console usage page](https://console.anthropic.com/usage) exposes token and request charts. For programmatic access, Anthropic provides a [Rate Limits API](https://platform.claude.com/docs/en/manage-claude/rate-limits-api) that returns org and workspace rate limit configurations. This is separate from the OAuth usage endpoint — it covers API-key accounts, not Max/Pro subscriptions.

---

## Known gotchas

**`seven_day` blocks access even when `seven_day_sonnet` has headroom.** Since Sonnet usage drains both counters simultaneously, `seven_day` reaches 100% first for heavy Sonnet users. The `seven_day_sonnet` counter becomes a misleading indicator — it shows remaining capacity that is not actually accessible. Reported in [#57875](https://github.com/anthropics/claude-code/issues/57875), open as of May 2026.

**`five_hour.resets_at` is not a hard boundary.** It reflects when the oldest usage peak expires from the rolling window. Usage that accumulates evenly across the window will have multiple expiry points, not a single one. The field underrepresents how quickly a heavily-used window will recover.

**`extra_usage` errors can fire despite available plan quota.** Several users have hit "You're out of extra usage" errors even when `seven_day` and `five_hour` show available headroom ([#45287](https://github.com/anthropics/claude-code/issues/45287), [#48274](https://github.com/anthropics/claude-code/issues/48274)). Appears to be a race condition or metering bug in the extra usage subsystem. Workaround: re-authenticate (`/logout` then `/login` in Claude Code).

**OAuth-proxied requests may be metered as extra usage.** Third-party tools that proxy the OAuth token but route through their own API gateway can cause requests to land in the `extra_usage` bucket instead of the plan's included quota. This depletes the extra usage balance even when the user has not intentionally enabled overage billing. [Source: CLIProxy issue](https://github.com/router-for-me/CLIProxyAPI/issues/2599)

**The usage endpoint itself rate-limits hard with no `Retry-After`.** Described in detail above. Do not poll more frequently than every 5 minutes. When you receive a 429 from the usage endpoint, back off for at least 5 minutes before retrying — shorter retries compound the issue and can trigger a lock-out that persists 30+ minutes.

**Token bucket algorithm for API limits.** API-key rate limits (RPM, ITPM, OTPM) use a continuous token bucket, not a per-minute reset. A burst of 60 requests in 1 second at a 60 RPM limit will trigger a 429 even though the per-minute average is fine. Plan for ~1 req/sec at lower tiers.

**`anthropic-ratelimit-tokens-*` reflects the most restrictive current limit.** If workspace limits are tighter than org limits, the headers reflect the workspace limit. This can be surprising when debugging limits in a shared workspace.

**Acceleration limits exist above rate limits.** A sharp traffic ramp — even within tier rate limits — can trigger a separate acceleration limit. The same 429 code is returned, and the `retry-after` header applies. Ramp traffic gradually; a 10× jump in QPS in a short window will trigger this even if you are far under your RPM ceiling. [Source: platform.claude.com/docs/en/api/rate-limits](https://platform.claude.com/docs/en/api/rate-limits)

---

## Sources

- [platform.claude.com/docs/en/api/rate-limits](https://platform.claude.com/docs/en/api/rate-limits) — official API rate limits, tier tables, response headers
- [claude.com/pricing](https://claude.com/pricing) — official pricing page
- [support.claude.com — usage limit best practices](https://support.claude.com/en/articles/9797557-usage-limit-best-practices) — 5-hour window explanation
- [support.claude.com — extra usage](https://support.claude.com/en/articles/12429409-manage-extra-usage-for-paid-claude-plans) — extra usage credits
- [TechCrunch — weekly rate limits announcement, July 28 2025](https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/) — original weekly limit announcement with plan-specific hour budgets
- [frozenlight.ai — weekly limits measured in hours](https://www.frozenlight.ai/post/frozenlight/710/anthropic-sets-weekly-usage-limits-measured-in-hours/) — per-plan hour budget table
- [pasqualepillitteri.it — May 15 2026 reset event](https://pasqualepillitteri.it/en/news/2614/claude-resets-rate-limits-5-hour-weekly-may-15-2026) — manual quota reset details and May 2026 promotion
- [openusage — Claude provider docs](https://github.com/robinebers/openusage/blob/main/docs/providers/claude.md) — community-documented `/api/oauth/usage` schema
- [claude-code issue #31021](https://github.com/anthropics/claude-code/issues/31021) — OAuth usage endpoint 429 rate limit bug
- [claude-code issue #31637](https://github.com/anthropics/claude-code/issues/31637) — aggressive rate limiting on usage endpoint
- [claude-code issue #57875](https://github.com/anthropics/claude-code/issues/57875) — `seven_day_sonnet` non-functional when `seven_day` exhausted
- [claude-code issue #51222](https://github.com/anthropics/claude-code/issues/51222) — weekly reset time display bug
- [apidog.com — weekly rate limits guide](https://apidog.com/blog/weekly-rate-limits-claude-pro-max-guide/) — reset mechanics overview

---

## Last updated

2026-05-15
