# OpenCode (Go subscription) — Usage Limits

Last updated: 2026-05-15
Sources: [opencode.ai/go](https://opencode.ai/go), [opencode.ai/docs/go/](https://opencode.ai/docs/go/),
[opencode.ai/docs/zen/](https://opencode.ai/docs/zen/), [opencode.ai/docs/providers/](https://opencode.ai/docs/providers/),
[github.com/anomalyco/opencode](https://github.com/anomalyco/opencode)

---

## What is OpenCode

OpenCode is an open-source AI coding agent that runs in your terminal, IDE, or as a desktop app. It is the spiritual successor to the earlier SST-maintained project (the GitHub org is now `anomalyco`, built by a company called **Anomaly**). The tool is comparable to Aider or Cursor's agent mode: it reads your codebase, runs edits, executes shell commands, and loops on feedback. It is primarily a TUI (terminal UI) but also ships a desktop wrapper.

OpenCode ships with support for 75+ providers via [Models.dev](https://models.dev), meaning you can plug in any API key you already have. The **Go subscription** is separate from all of that: it is OpenCode's own hosted, curated set of open-weight models billed through OpenCode's infrastructure, with no API key required from the user.

**Hosting model for Go models.** The docs state models are "hosted in the US, EU, and Singapore for stable global access" with a zero-data-retention policy. OpenCode does not publicly name the underlying inference provider (Together AI, Fireworks, etc.) — they present it as their own managed surface. Requests go to OpenCode's servers and are proxied to wherever the model actually runs.

**What makes Go distinct from free.** Free gives you "Big Pickle" and a small set of other free Zen models with heavy rate limiting. Go unlocks 12 high-quality open-weight models (GLM, Kimi, DeepSeek, MiniMax, MiMo, Qwen) under a dollar-credit quota system. Go is currently in beta; the model list, quotas, and pricing are subject to change without notice.

---

## Subscription tiers

| Tier | Price (USD/mo) | What you get | Notes |
|------|---------------|--------------|-------|
| **Free** | $0 | Big Pickle + a few other Zen free models, heavy rate limits | Big Pickle: 200 requests per 5h; Nemotron 3 Super Free: trial only |
| **Go** | $5 first month, then $10/mo | 12 curated open-weight models, $12/$30/$60 credit windows | Beta; model list changes; Zen balance fallback optional |
| **Zen (pay-as-you-go)** | $0 base + usage | 40+ models including commercial (GPT-5.5, Claude Opus 4.7, Gemini) billed per token | Auto-reload $20 when balance < $5; monthly spend caps available |

Go and Zen are separate products that live on the same account. You can have both active. Go is the fixed-price plan; Zen is a top-up wallet for when you want commercial models or want to overflow Go limits.

---

## Free tier — what's actually free

The free tier gives access to models hosted through **OpenCode Zen** at no cost, at least during limited beta windows. As of 2026-05-15 the documented free Zen models are:

- **Big Pickle** — OpenCode's own stealth model, optimized for coding agents. 200K context window. Fast for file edits, function implementation, code review. Described as a "stealth model" — the underlying weights are not publicly disclosed. Offered free while the team collects feedback; data from free usage may be used for model improvement. Available at 200 requests per 5-hour window (regardless of Go subscription).
- **DeepSeek V4 Flash Free** — a free variant of DeepSeek V4 Flash with reduced quota.
- **Nemotron 3 Super Free** — trial-use only, explicitly marked not for production or sensitive data.

If you hit your Go usage limit mid-session, OpenCode falls back to free models automatically so the session does not hard-fail.

Rate limits on the free tier are not precisely documented. Expect 200 requests per 5h for Big Pickle based on the table at opencode.ai/go.

---

## Go subscription — full model and quota table

Quotas are derived from a **dollar-credit pool**: $12 per rolling 5h window, $30 per rolling week, $60 per rolling month. The request counts below are derived from these dollar amounts divided by OpenCode's per-model cost estimate (see token shapes below). They are estimates, not hard byte counters.

Source: [opencode.ai/docs/go/](https://opencode.ai/docs/go/) — verified 2026-05-15. Numbers match the user-provided table exactly.

| Model | Requests / 5h | Requests / week | Requests / month |
|-------|-------------:|----------------:|-----------------:|
| GLM-5.1 | 880 | 2,150 | 4,300 |
| GLM-5 | 1,150 | 2,880 | 5,750 |
| Kimi K2.5 | 1,850 | 4,630 | 9,250 |
| Kimi K2.6 | 1,150 | 2,880 | 5,750 |
| MiMo-V2.5 (≤256K ctx) | 2,150 | 5,450 | 10,900 |
| MiMo-V2.5-Pro | 1,290 | 3,225 | 6,450 |
| MiniMax M2.7 | 3,400 | 8,500 | 17,000 |
| MiniMax M2.5 | 6,300 | 15,900 | 31,800 |
| Qwen3.6 Plus | 3,300 | 8,200 | 16,300 |
| Qwen3.5 Plus | 10,200 | 25,200 | 50,500 |
| DeepSeek V4 Pro | 3,450 | 8,550 | 17,150 |
| DeepSeek V4 Flash | 31,650 | 79,050 | 158,150 |

**Important caveats on the counts.** These numbers are back-calculated from the dollar windows using average token shapes (see next section). Actual quota consumed per request varies with your real prompt length. A conversation with a very large context or long output will cost more than the average and hit the limit before the listed count. Conversely, short prompts will get more requests.

Context windows are not officially documented per-model on the Go page. MiMo-V2.5 is explicitly listed as "≤256K" which suggests context has an upper bound on Go (vs potentially longer on the raw model API).

---

## Token shape estimates

OpenCode publishes estimated per-request token shapes to explain how the dollar quotas translate to request counts. These are observed averages from typical coding-agent sessions — they assume heavy prompt caching (see Caching note below).

Source: [opencode.ai/docs/go/](https://opencode.ai/docs/go/)

| Model(s) | Input tokens | Cached tokens | Output tokens |
|----------|-------------:|--------------:|--------------:|
| GLM-5, GLM-5.1 | 700 | 52,000 | 150 |
| Kimi K2.5, Kimi K2.6 | 870 | 55,000 | 200 |
| DeepSeek V4 Pro | 750 | 82,000 | 290 |
| DeepSeek V4 Flash | 790 | 68,000 | 280 |
| MiniMax M2.7, M2.5 | 300 | 55,000 | 125 |
| MiMo-V2.5 (≤256K) | 1,000 | 60,000 | 140 |
| MiMo-V2.5-Pro | 350 | 41,000 | 250 |
| Qwen3.5 Plus | 410 | 47,000 | 140 |
| Qwen3.6 Plus | 500 | 57,000 | 190 |

**How the math works.** For each model, OpenCode multiplies token counts by the model's per-token price (input, cached-read, and output are billed differently). The result is a per-request dollar cost. Dividing the window budget ($12, $30, $60) by that per-request cost gives the published request count. Cached tokens are billed at significantly lower rates than fresh input tokens — this is why models with large cached-token counts (DeepSeek V4 Pro: 82K cached) can have relatively low request counts despite being "cheap" by standard API pricing.

**Caching note.** The 50–82K cached token values are large, implying OpenCode or the underlying provider runs aggressive prompt caching on Go requests. If a session starts cold (no cache hit), the effective per-request cost is higher and the actual request count you get will be lower than the table suggests. There is no published per-session cache-warmup behavior. This is one of the main reasons the published request counts should be treated as rough guides, not guaranteed allowances.

---

## Reset windows

All three limits are **rolling windows**, not fixed calendar resets.

- **5-hour window**: rolls forward continuously. If you make a request, the token cost falls off the 5h window exactly 5 hours later. OpenCode's web dashboard shows the remaining time until the window next clears.
- **Weekly window**: rolls on a 7-day cycle from the time you first used Go in the period. Timezone behavior is unclear — GitHub issues report some inconsistency in countdown display (see [#22015](https://github.com/anomalyco/opencode/issues/22015)).
- **Monthly window**: rolls on a 30-day cycle, not a calendar-month reset.

The dashboard at opencode.ai shows rolling, weekly, and monthly usage as percentages with "resets in N minutes" counters. There is no CLI or TUI command to query this data directly as of 2026-05-15 (see Monitoring section).

---

## Addon subscriptions (bring-your-own)

OpenCode supports several models of provider authentication beyond Go. These are configured through the **Providers** section of OpenCode's settings, not through the Go subscription.

### ChatGPT Plus/Pro (OpenAI Codex)

OpenCode supports ChatGPT Plus and Pro via OAuth passthrough. In the provider setup, select "ChatGPT Plus/Pro" and OpenCode opens your browser to authenticate against your OpenAI account. Your ChatGPT subscription quota is then used directly. No API key required. This works as long as OpenAI permits it — subject to OpenAI's terms on third-party use of ChatGPT subscriptions.

Source: [opencode.ai/docs/providers/](https://opencode.ai/docs/providers/)

### Google Gemini

Google Gemini can be used via API key (from Google AI Studio or Vertex AI). OAuth passthrough for a Google One AI Premium subscription is not mentioned in the current docs — Gemini is listed as an API-key provider, not an OAuth-passthrough provider.

For high volume, the Zen pay-as-you-go wallet also lists Gemini models at token-based pricing.

### Claude (Anthropic)

**This changed on January 9, 2026.** Anthropic blocked third-party tools from using Claude Pro/Max OAuth tokens. Before that date, OpenCode could authenticate against your Claude subscription via browser OAuth. After that date, Claude requires a standard Anthropic API key.

Workarounds exist in the community: [opencode-with-claude](https://github.com/ianjwhite99/opencode-with-claude) (Meridian local proxy, maps OpenCode's Anthropic-style HTTP to the Claude Agent SDK via your `claude login` session), and [oc-go-cc](https://github.com/samueltuyizere/oc-go-cc) (routes Go subscription traffic to Claude Code). These are unofficial and not supported by OpenCode directly. Anthropic's position on these workarounds is not documented.

Through Zen, Claude Opus 4.7 is available at $5 input / $25 output per million tokens (pay-as-you-go from your Zen balance).

### GitHub Copilot and GitLab Duo

Both supported via OAuth passthrough — device-code flow for Copilot, browser OAuth for GitLab Duo. Uses your existing subscription quota. These are the most reliably supported passthrough providers as of 2026-05-15.

### Summary table

| Provider | Auth method | Uses your subscription? | Notes |
|----------|-------------|------------------------|-------|
| Go models (GLM/Kimi/etc.) | OpenCode account | Go is the subscription | $10/mo |
| ChatGPT Plus/Pro | OAuth browser | Yes | Subject to OpenAI ToS |
| GitHub Copilot | OAuth device code | Yes | Well-supported |
| GitLab Duo | OAuth browser | Yes | — |
| Claude | API key only (as of 2026-01-09) | No | Anthropic blocked OAuth; workarounds exist but unofficial |
| Gemini | API key | No | Or Zen pay-as-you-go |
| 75+ others | API key via Models.dev | No | BYOK |

---

## Monitoring — what claw-fleet could extract

### Current state (2026-05-15): limited

There is **no public API endpoint** for Go plan usage data. The dashboard at opencode.ai shows rolling/weekly/monthly usage percentages and reset timers, but this is rendered server-side with no documented REST endpoint.

A feature request is open and well-articulated at [anomalyco/opencode#16017](https://github.com/anomalyco/opencode/issues/16017), proposing `GET /api/v1/usage/plan` with usage percent and resets_in_seconds per window. As of the issue filing (2026-03-04) no response from the team is documented. File a +1 on that issue to increase priority.

A related issue for Zen credit balance API is at [#10448](https://github.com/anomalyco/opencode/issues/10448). Unified provider-level usage tracking is at [#9281](https://github.com/anomalyco/opencode/issues/9281).

### Local state files

Credentials are stored at `~/.local/share/opencode/auth.json`. This file holds authentication tokens but not usage counters. No `~/.local/share/opencode/usage.json` or equivalent is documented or known to exist.

### Rate-limit headers

When a Go request hits the limit, OpenCode returns a 429-equivalent. The TUI shows a countdown to the next window reset. Whether the underlying HTTP response includes `X-RateLimit-*` or `Retry-After` headers is undocumented and has not been confirmed in any GitHub issue as of 2026-05-15.

### Zen models endpoint

One programmatic hook does exist: `GET https://opencode.ai/zen/v1/models` returns available Zen models and metadata. This can be queried with a Zen API key to see what models are available, but it does not return usage data.

### Helicone integration

OpenCode supports Helicone headers (`Helicone-User-Id`, `Helicone-Session-Id`) for observability. If you route Go requests through a Helicone-compatible proxy, you could capture token counts and costs independently. This is the most viable current monitoring path for accurate per-request cost tracking, though it requires setting up Helicone (free tier available).

### Recommended claw-fleet approach

Until [#16017](https://github.com/anomalyco/opencode/issues/16017) ships:

1. **Dashboard polling**: Authenticate against opencode.ai and scrape the dashboard page for the usage percentages. Fragile (markup can change), but the only option without an API.
2. **Helicone sidecar**: Configure OpenCode to send requests through a local Helicone-compatible proxy that logs token usage.
3. **429 detection**: Monitor for 429 responses in the OpenCode session log and infer "window exhausted" events.
4. **File a feature request**: +1 on #16017. The data is already computed server-side; exposing it is small work.

---

## Known gotchas

**Model list changes without warning.** OpenCode Go is in beta. Models have been added and removed. The 12 models in the table above match the live docs page as of 2026-05-15, but may differ by the time you read this. Always check [opencode.ai/docs/go/](https://opencode.ai/docs/go/) for the current list.

**Request counts are estimates, not guarantees.** The table is derived from observed average token shapes. A session with unusually long context or verbose output will hit the dollar limit before the listed request count. Treat the counts as a ballpark. The actual limiting mechanism is the dollar value, not the request count.

**Cold cache = fewer requests.** The token shape estimates assume heavy prompt caching (50–82K cached tokens per request). A fresh session with no cache has higher effective per-request costs. Early requests in a session are more expensive than later ones once the cache warms. This is not documented in the official limits table.

**3 windows are all active simultaneously.** Hitting any one of the three (5h, weekly, monthly) stops Go requests. The 5h window is the binding constraint for burst usage; the monthly is the ultimate ceiling. The $60 monthly cap is larger than the $10 subscription price — the extra $50 is the "burst allowance" baked in for heavy months.

**Anthropic Claude OAuth blocked (2026-01-09).** If you were previously using Claude Pro/Max through OpenCode via browser OAuth, that stopped working on January 9, 2026. Anthropic revoked third-party OAuth access. You need an API key, a Zen balance for Claude models, or an unofficial proxy workaround.

**Zen balance and Go are separate wallets.** Accidentally enabling "Use balance" in the Go console will draw down your Zen credits once Go limits are hit. This is opt-in but easy to forget. Monitor your Zen balance independently if you enable this fallback.

**The $10/month subscription vs $60/month usage cap.** The subscription buys you access; the $60 usage cap is the dollar value of requests OpenCode will serve you at no extra charge. You pay $10 regardless of whether you use $0 or $60 of compute. Going over the windows blocks requests until the window resets — there is no overage billing on Go.

---

## 2026-05-21 update: queryable surfaces

### Dashboard URL

The usage dashboard lives at `https://opencode.ai/workspace/{workspaceID}/go` — a Next.js server-rendered page that embeds the usage data in inline script tags as a JSON-like object. There is no separate `/console` or `/dashboard` route; the workspace Go page is the surface.

The workspaceID has the format `wrk_XXXXXXXXXX`. Users who have visited the workspace in a browser can find it in their browser's history at `opencode.ai/workspace/`.

### What the page embeds

The page's inline JS contains an object with three fields parsed by the community-built [opencode-bar](https://github.com/opgginc/opencode-bar) project:

```
rollingUsage.usagePercent    (float)      rolling 5h window, % consumed
rollingUsage.resetInSec      (int)        seconds until the 5h window next clears
weeklyUsage.usagePercent     (float)
weeklyUsage.resetInSec       (int)
monthlyUsage.usagePercent    (float)
monthlyUsage.resetInSec      (int)
```

There is no stable JSON REST endpoint for this data as of 2026-05-21. The proposed `GET /zen/go/v1/usage` endpoint from PR [#16513](https://github.com/anomalyco/opencode/issues/16017) was still open and unmerged as of May 19, 2026.

### Auth mechanism

The dashboard uses **session cookie auth**. The relevant cookie is named `auth` on the `opencode.ai` domain. Auth is established via OAuth (GitHub or Google) at `auth.opencode.ai`, which sets a session cookie and redirects back to the workspace URL.

There is no API key path for the dashboard itself. The OpenCode Go API key (from `~/.local/share/opencode/auth.json`, under the `opencode-go` provider entry) authorizes inference requests to `opencode.ai/zen/go/v1/*` but does not grant access to the workspace dashboard page.

To get the session cookie programmatically: the user visits `opencode.ai/workspace/{workspaceID}/go` in any browser while logged in. The `auth` cookie is readable from the browser's SQLite cookie store at the paths documented in opencode-bar's source.

### Probe design for claw-fleet

**Request:**
```
GET https://opencode.ai/workspace/{workspaceID}/go
Accept: text/html
Cookie: auth={session_cookie_value}
User-Agent: Mozilla/5.0 (compatible)
```

**Parse:** extract `rollingUsage`, `weeklyUsage`, `monthlyUsage` objects from the inline script. The markup uses HTML entity escaping; unescape `&quot;` and `&#34;` before parsing.

**Auth source:** read from `~/.config/claw-fleet/opencode-go.json` (keys: `workspaceId`, `authCookie`) or from environment variables `OPENCODE_GO_WORKSPACE_ID` / `OPENCODE_GO_AUTH_COOKIE`. Falls back to reading browser cookie stores (Chrome, Brave, Arc, Edge) and browser history for workspace URLs — the same discovery order as opencode-bar.

**Refresh cadence:** 60 seconds minimum (same as opencode-bar). The data does not change faster than that in practice.

**Failure mode:** if the cookie expires, the page returns a redirect to the login flow (HTTP 302 to `auth.opencode.ai`). Detect by checking response URL or HTTP status; surface as auth-expired in the widget and prompt the user to re-login at opencode.ai.

### One account = how many widget rows

**One row per account**, not one row per model. The dashboard gives three window percentages (5h, weekly, monthly) for the entire Go subscription — it is not broken down by model. The worst-case window (highest %) is the binding constraint, so the widget displays the max of the three as the headline utilization percentage, with all three windows available in the dropdown detail. This matches how opencode-bar presents it.

If you want to display absolute estimates alongside the percentage: multiply the percentage by the dollar window limit and divide by the per-model cost. Example: `rolling=47%` with a $12 5h window = $5.64 consumed. To get request-count estimates, apply the dollar-to-request ratios from the model table above. This is approximate since the actual mix of models used is unknown.

### Script outline: `src/bin/claw-fleet-opencode`

```
1.  load_config — read workspaceId + authCookie from ~/.config/claw-fleet/opencode-go.json
                  or env vars, or browser cookie discovery (lowest precedence)
2.  if no credentials → print "auth required" + exit 1
3.  fetch_dashboard — GET opencode.ai/workspace/{workspaceId}/go with Cookie: auth={cookie}
4.  if HTTP 302 → print "session expired: re-login at opencode.ai" + exit 2
5.  if HTTP not 200 → print "fetch error: {status}" + exit 3
6.  parse_usage — regex-extract rollingUsage, weeklyUsage, monthlyUsage objects from HTML
7.  unescape HTML entities in extracted text before numeric parsing
8.  compute max_percent = max(rolling%, weekly%, monthly%)
9.  format_output — emit JSON matching existing usage-cache.json schema:
      { "account": "OC1", "provider": "opencode-go",
        "rolling_pct": N, "weekly_pct": N, "monthly_pct": N,
        "max_pct": N, "rolling_resets_in": N, "weekly_resets_in": N,
        "monthly_resets_in": N, "fetched_at": "ISO8601" }
10. merge into ~/.claude/usage-cache.json (same pattern as claw-fleet poller for Anthropic accounts)
11. on parse error → log warning + preserve last-known data with staleness marker
```

Lines 1-11 fit in ~40 lines of shell or Python. No new dependencies beyond `curl` and `python3 -c`.

### Status of the official API endpoint

Feature request [anomalyco/opencode#16017](https://github.com/anomalyco/opencode/issues/16017) proposes `GET /api/v1/usage/plan` with Bearer auth and a clean JSON response. PR [#16513](https://github.com/anomalyco/opencode/pull/16513) implements `GET /zen/go/v1/usage` (same data, different path), copying the pattern from `/zen/v1/models`. The PR was open and unmerged as of May 19, 2026, with multiple +1 comments but no team acknowledgment. If it ships, replace the HTML scrape in step 3-8 above with a single `curl` to the endpoint using the Go API key as `Authorization: Bearer`.

### Helicone integration detail

OpenCode Go requests accept `Helicone-Auth`, `Helicone-User-Id`, and `Helicone-Session-Id` headers. To enable: set `HELICONE_API_KEY` in your environment and configure a Helicone proxy as your OpenCode base URL. Helicone's free tier records per-request token counts and costs. This is the only current path to per-model breakdown (vs. the aggregate percentage the dashboard provides). claw-fleet could read from the Helicone API (`GET https://www.helicone.ai/api/request`) as an optional enrichment pass.

---

## Sources

- [opencode.ai/go](https://opencode.ai/go) — Go subscription landing page with model table and quota numbers
- [opencode.ai/docs/go/](https://opencode.ai/docs/go/) — Full Go documentation: dollar windows, token shapes, reset behavior
- [opencode.ai/docs/zen/](https://opencode.ai/docs/zen/) — Zen pay-as-you-go documentation, free models, Big Pickle, credit system
- [opencode.ai/docs/providers/](https://opencode.ai/docs/providers/) — Provider integrations: ChatGPT OAuth, Copilot, GitLab, Claude API key
- [github.com/anomalyco/opencode](https://github.com/anomalyco/opencode) — Main repo (anomalyco org, built by Anomaly); 163K stars, TypeScript
- [github.com/anomalyco/opencode#16017](https://github.com/anomalyco/opencode/issues/16017) — Feature request: Go plan usage API endpoint (open as of 2026-05-15)
- [github.com/anomalyco/opencode#15872](https://github.com/anomalyco/opencode/issues/15872) — Clarification on $10/month vs $60/month confusion (closed)
- [github.com/anomalyco/opencode#25030](https://github.com/anomalyco/opencode/issues/25030) — Feature request: in-TUI quota check command (open)
- [github.com/ianjwhite99/opencode-with-claude](https://github.com/ianjwhite99/opencode-with-claude) — Unofficial Meridian proxy for Claude Max/Pro with OpenCode
- [github.com/samueltuyizere/oc-go-cc](https://github.com/samueltuyizere/oc-go-cc) — Unofficial Go-to-Claude-Code bridge
- [help.apiyi.com — Is OpenCode Go Worth It?](https://help.apiyi.com/en/opencode-go-subscription-worth-it-review-en.html) — Independent review with dollar-window explanation
- [blog.patshead.com — OpenCode Go from a light user's perspective](https://blog.patshead.com/2026/03/opencode-go-coding-plan-from-a-light-users-perspective.html) — Community user analysis

---

## claw-fleet integration

### Required vault vars

Add both of the following to `~/.claude/vault.env`:

```bash
OPENCODE_GO_WORKSPACE_ID="wrk_XXXXXXXXXX"
OPENCODE_GO_AUTH_COOKIE="your-auth-cookie-value-here"
```

Without these, the probe emits a `no_auth` row and the widget shows the account with a gray "auth required" state. The LaunchAgent still loads and runs every 5 minutes — it just waits for the credentials.

### How to get the credentials from Chrome

**Workspace ID:**

1. Log in at opencode.ai in Chrome.
2. Navigate to your Go dashboard. The URL will be `https://opencode.ai/workspace/wrk_XXXXXXXXXX/go`.
3. Copy the `wrk_XXXXXXXXXX` portion — that is your `OPENCODE_GO_WORKSPACE_ID`.

**Auth cookie:**

1. While on `opencode.ai/workspace/.../go`, open DevTools (F12 or Cmd+Option+I).
2. Go to the Application tab, then Storage > Cookies > `https://opencode.ai`.
3. Find the cookie named `auth`. Copy its Value field.
4. Paste that value as `OPENCODE_GO_AUTH_COOKIE` in vault.env.

The `auth` cookie is a session token set by `auth.opencode.ai` after OAuth login (GitHub or Google). It does not expire on a fixed schedule but will become invalid if you log out, if OpenCode rotates sessions, or if the browser clears cookies. When it expires the probe emits `auth_expired` in the widget; re-export the cookie to restore monitoring.

### Refresh cadence

The LaunchAgent runs `claw-fleet-opencode` every 300 seconds (5 minutes). This is well above the opencode-bar recommended minimum of 60 seconds and avoids unnecessary load on the opencode.ai servers. The probe fetches one HTML page per run; no API key or OAuth token is involved beyond the session cookie.

### Cache slot mapping

The OpenCode Go dashboard exposes three usage windows: rolling 5h, weekly 7d, and monthly. The claw-fleet cache schema has two cross-window slots (`five_hour`, `seven_day`) and a model-specific slot (`seven_day_sonnet`) that is unused for OpenCode accounts. The mapping is:

| Dashboard window | Cache field | Notes |
|---|---|---|
| Rolling 5h (rollingUsage) | `usage.five_hour` | utilization, resets_at, and (since v0.1.8) per-slot `status` ("ok" / "rate-limited"). |
| Weekly 7d (weeklyUsage) | `usage.seven_day` | Same shape. The cap signal for OpenCode rows is `status == "rate-limited"` first, then utilization >= 100 fallback. |
| Monthly (monthlyUsage) | `usage.seven_day_sonnet` | Repurposed slot. The widget's "Son" column is monthly% for OpenCode rows; tooltips on the row label clarify this per-provider. |

### Persisted `opencode_meta` block (added v0.1.9)

In addition to the usage slots, the poller writes an `opencode_meta` block on every successful probe. It carries the Go-plan dollar context and the user's billing / unblock state. Shape:

```json
{
  "balance": 0,
  "use_balance": false,
  "monthly_limit": null,
  "subscription_plan": null,
  "lite_subscription_id": "sub_XXXXXXXXX",
  "is_admin": true,
  "limits_usd": { "rolling": 12, "weekly": 30, "monthly": 60 },
  "spent_usd":  { "rolling": 0.0, "weekly": 30.0, "monthly": 30.0 }
}
```

| Field | Source | Notes |
|---|---|---|
| `balance` | RSC payload `balance` | Zen wallet, in cents. Independent of Go subscription. |
| `use_balance` | RSC payload `useBalance` | When true, Go falls back to Zen balance once dollar limits hit. Defaults off. |
| `monthly_limit` | RSC payload `monthlyLimit` | User-set monthly cap; null on Go default. |
| `subscription_plan` | RSC payload `subscriptionPlan` | "pro"/"max"/null. Null = lite/Go. Used to detect plan upgrades. |
| `lite_subscription_id` | RSC payload `liteSubscriptionID` | Stripe subscription ID. |
| `is_admin` | RSC payload `isAdmin` | Workspace admin role. |
| `limits_usd` | Hardcoded from docs | $12 / $30 / $60. Constant unless OpenCode changes the plan structure. |
| `spent_usd` | Computed | `percent * limit / 100` per window, rounded to two decimals. |

The `note` field on every `ok` row reads e.g. `"go: 5h $0/$12 • 7d $30/$30 rate-limited • mo $30/$60"`. If `balance > 0` it gets appended (`• bal $X.XX`).

If the cache schema gains a dedicated monthly column in a future version, migrate the mapping (drop the seven_day_sonnet repurpose) and update the widget Son-column tooltip.

### Widget tooltips (added v0.1.9)

Both the Übersicht widget and the native macOS panel surface OpenCode-specific context on the row-label tooltip:

```
alisalaah@gmail.com — OpenCode Go plan
5h:  $0 / $12
7d:  $30 / $30 RATE-LIMITED
mo:  $30 / $60
Zen balance: $0.00 • useBalance: off
Unblock: top up Zen balance + enable Use balance.
(Son column = monthly% for OpenCode rows.)
```

The unblock line is conditional: it appears only when the weekly slot is capped and tailors to the balance state (top-up needed vs. just toggle useBalance ON).

The SwiftBar plugin renders dollar-denominated rows in the per-account detail submenu instead of the original percent-only layout when the row is an OpenCode account.

### Cookie expiry caveat

Session cookies for `opencode.ai` have no published TTL. In practice they remain valid for weeks to months, but can invalidate on: logging out via the web UI, OpenCode security rotation events, or browser cookie-jar clearing. The widget displays `auth_expired` when this happens. To recover: log in at opencode.ai in Chrome, re-export the `auth` cookie from DevTools, update vault.env, and the next 5-minute probe run will restore the `ok` status automatically.
