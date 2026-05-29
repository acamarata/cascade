# Gemini (Google) — Usage Limits

> **Last updated:** 2026-05-15

---

## The three surfaces (overview)

Google ships three products under the "Gemini" brand that have almost nothing in common from a quota perspective. The **consumer app** (gemini.google.com, mobile apps) gives you a chat interface under Google AI Pro/Ultra subscriptions — there is no public API, no usage endpoint, and no programmatic way to read how many prompts you have left. The **Gemini API** (ai.google.dev, Google AI Studio) is a completely separate developer product billed per token on a GCP project; you sign up with an API key, and quota is queryable via Cloud Monitoring. The **Vertex AI / Code Assist** tier is GCP-native, enterprise-billed, and lives at a different hostname entirely. Paying for AI Pro gives you nothing on the API side; the two billing systems are independent even on the same Google account.

---

## Surface 1: Consumer Gemini (Google AI Pro / Ultra)

### Subscription tiers (as of May 2026, Google I/O announcements)

| Plan | Price | Usage relative to Free |
|---|---|---|
| Free | $0 | Baseline ("standard limits") |
| AI Plus | ~$8/mo | 2× standard |
| AI Pro | $19.99/mo (≈$200/yr) | 4× standard |
| AI Ultra (entry) | $100/mo | 5× AI Pro |
| AI Ultra (full) | $200/mo (reduced from $250) | 20× AI Pro |

Sources: [Google One AI Plans](https://one.google.com/about/google-ai-plans/), [Google I/O 2026 subscription post](https://blog.google/products-and-platforms/products/google-one/google-ai-subscriptions/), [Android Headlines pricing](https://www.androidheadlines.com/2026/05/google-ai-subscriptions-ultra-pro-price-drop-features.html).

### How limits work after May 17, 2026

Google abandoned fixed per-day message counts in favor of a **compute-based credit model**. Your allowance is now consumed by prompt complexity, thinking levels, features used (Deep Research, image gen, video), and conversation length. Limits refresh every five hours until you hit a weekly cap. No specific weekly number is published — Google only discloses relative multipliers.

**Before this change:** AI Pro users had a documented cap of ~100 Gemini 2.5 Pro prompts/day. Some users reported this silently dropped to ~25/day before the compute model replaced it entirely (community thread: [Gemini Pro usage limit dropped 100→25](https://support.google.com/gemini/thread/411457870)). Google never publicly confirmed the 25-prompt interim cap.

Sources: [PCWorld on usage limit changes](https://www.pcworld.com/article/3142744/google-just-made-big-changes-to-gemini-usage-limits.html), [Android Authority AI Pro downgrade](https://www.androidauthority.com/google-ai-pro-usage-limits-3669063/), [Android Central on the quiet downgrade](https://www.androidcentral.com/apps-software/ai/google-ai-pro-plan-just-got-a-quiet-downgrade).

### Other capped features

Google's help article lists that Deep Research reports, image generation, and video outputs all consume the same compute pool — there is no separate per-day counter for these features under the new model. The prior system had distinct caps (e.g., a fixed number of Deep Research queries/day), but Google has not re-published per-feature numbers under the new system.

### Reset window

Limits refresh on a **5-hour rolling window** until you exhaust the weekly budget. This replaced the previous midnight Pacific daily reset.

### No public usage API — critical constraint for monitoring

**There is no endpoint to query how many credits you have consumed or how many remain.** The only way to see your usage is inside the Gemini app under Settings → Usage limits. No cookie-based scraping can extract this reliably; Google does not surface the counter in any page DOM that survives their frequent internal changes.

What claw-fleet can do: verify session liveness by checking whether a request to `gemini.google.com/app` with an authenticated session returns a valid response. Authentication relies on the `__Secure-1PSID`, `__Secure-1PSIDCC`, `__Secure-1PSIDTS`, and `NID` session cookies, not `SAPISIDHASH` (which is used for other Google properties). Cookie sets expire and rotate; the liveness check is a binary alive/dead signal only.

Google's own support documentation for AI Pro ([support.google.com/googleone/answer/14534406](https://support.google.com/googleone/answer/14534406)) does not mention any API or programmatic access to usage data. The feature is a consumer product.

### AI Pro / Ultra does NOT include API access

Paying for AI Pro or AI Ultra does not give you Gemini API tokens, does not create API keys, and does not lift API rate limits. These are separate billing systems on the same Google account. You must set up API access independently through AI Studio. As of April 2026 Google added a benefit where Pro/Ultra subscribers get higher limits in AI Studio's free-tier mode (the "Google AI" billing option in the Studio sidebar), but production API keys still require a paid GCP billing account separate from the subscription.

Source: [9to5Google on AI Studio limits](https://9to5google.com/2026/04/20/google-ai-studio-limits/), [developer forum thread on Veo 3 API access](https://discuss.ai.google.dev/t/does-google-one-ai-pro-include-api-access-for-veo-3-or-is-it-limited-to-flow-only/110794), [Google AI Studio Pro blog post](https://blog.google/innovation-and-ai/technology/developers-tools/google-one-ai-studio/).

---

## Surface 2: Gemini API (Google AI Studio, ai.google.dev)

This is the programmatic API at `generativelanguage.googleapis.com`. Accessed via API key from [aistudio.google.com](https://aistudio.google.com). Completely separate from the consumer subscriptions above.

### Usage tiers and spending thresholds

| Tier | Requirement | Default spend cap |
|---|---|---|
| Free | Active GCP project | N/A (no billing) |
| Tier 1 | Active billing account attached | $250 |
| Tier 2 | $100+ spent, ≥3 days on billing | $2,000 |
| Tier 3 | $1,000+ spent, ≥30 days on billing | $20,000–$100,000+ |

Tier 1 upgrades take effect immediately after adding billing. Tier 2 and 3 require elapsed time plus cumulative spend. Source: [ai.google.dev/gemini-api/docs/rate-limits](https://ai.google.dev/gemini-api/docs/rate-limits).

### Free tier rate limits

Google no longer publishes a static per-model table in their rate limits doc; they redirect to the live dashboard at `https://aistudio.google.com/rate-limit`. Third-party sources reporting observed values (as of early 2026, note Google cut free tier 50–80% in December 2025):

| Model | RPM | TPM | RPD |
|---|---|---|---|
| gemini-2.5-pro | ~5 | 250,000 | 50–100 |
| gemini-2.5-flash | ~10–15 | 250,000–1,000,000 | 250–1,500 |
| gemini-2.5-flash-lite | ~15 | 1,000,000 | 1,000–1,500 |
| gemini-1.5-pro | ~2 | 250,000 | 50 |

These numbers vary by project and have shifted; treat them as estimates. The authoritative source is your project's rate limit dashboard in AI Studio.

Sources: [pecollective free tier guide](https://pecollective.com/tools/gemini-free-tier-guide/), [aifreeapi.com rate limit reference](https://www.aifreeapi.com/en/posts/gemini-api-free-tier-rate-limits), [laozhang AI blog on free tier](https://blog.laozhang.ai/en/posts/gemini-api-free-tier).

**December 2025 cut:** Google reduced free tier quotas by 50–80% on December 7, 2025. Source: [aifreeapi.com](https://www.aifreeapi.com/en/posts/gemini-api-free-tier-rate-limits).

### Paid tier rate limits

At Tier 1, limits increase substantially (e.g., ~150 RPM for gemini-2.5-pro, making it viable for production). Enterprise customers at Tier 3 typically see 4,000+ RPM and 4M+ TPM. Exact numbers are per-project and visible in AI Studio. Source: [yingtu.ai rate limits guide](https://yingtu.ai/en/blog/gemini-api-rate-limits-explained).

### Project Spend Caps (announced March 16, 2026)

Google added per-project monthly dollar caps configurable in AI Studio (Spend tab → Monthly spend cap). Once set, the cap is persistent until you modify it. There is a ~10-minute enforcement delay, so minor overages are possible during a burst. There is no API to set spend caps programmatically; it is UI-only in AI Studio. Source: [Google blog on cost controls](https://blog.google/innovation-and-ai/technology/developers-tools/more-control-over-gemini-api-costs/).

### Reset windows

- **Per-minute limits (RPM, TPM):** rolling 60-second window
- **Per-day limits (RPD):** reset at **midnight Pacific time**

### Monitoring: Cloud Monitoring API

For projects with billing enabled, quota consumption is queryable via the GCP Cloud Monitoring API.

**Metric type strings (serviceruntime layer):**

```
serviceruntime.googleapis.com/quota/rate/net_usage
serviceruntime.googleapis.com/quota/allocation/usage
serviceruntime.googleapis.com/quota/exceeded
```

Filter to the Gemini API service:

```
metric.type="serviceruntime.googleapis.com/quota/rate/net_usage"
resource.type="consumer_quota"
resource.label.service="generativelanguage.googleapis.com"
```

**Native generativelanguage metrics** (from error messages and forum posts — these are the quota metric labels you see in 429 responses):

```
generativelanguage.googleapis.com/generate_content_free_tier_requests
generativelanguage.googleapis.com/generate_content_free_tier_input_token_count
generativelanguage.googleapis.com/generate_content_requests
```

Source: [developer forum 429 thread](https://discuss.ai.google.dev/t/quota-exceeded-for-metric-generativelanguage-googleapis-com-generate-content-free-tier-requests-limit-50/105796), [GCP monitoring quota metrics guide](https://docs.cloud.google.com/monitoring/alerts/using-quota-metrics).

**Example Python query (Cloud Monitoring v3):**

```python
from google.cloud import monitoring_v3
from google.cloud.monitoring_v3 import query

client = monitoring_v3.MetricServiceClient()
# Query net quota rate usage for generativelanguage
filter_str = (
    'metric.type="serviceruntime.googleapis.com/quota/rate/net_usage"'
    ' resource.type="consumer_quota"'
    ' resource.labels.service="generativelanguage.googleapis.com"'
)
# Use timeSeries.list with an interval covering the last hour
```

Auth: Application Default Credentials (ADC) via `gcloud auth application-default login`, or a service account with `roles/monitoring.viewer`.

**Cloud Quotas API** (`cloudquotas.googleapis.com`): Can list current quota limits and request adjustments. Does not provide real-time consumption metrics; use Cloud Monitoring for that.

**gcloud equivalent:**

```bash
gcloud services quotas list \
  --service=generativelanguage.googleapis.com \
  --project=YOUR_PROJECT_ID
```

### AI Studio built-in dashboard

AI Studio has a rate limit dashboard at `https://aistudio.google.com/rate-limit?timeRange=last-28-days` showing RPM, TPM, and RPD usage per project with graphs. No API access to this dashboard; it is read-only UI.

---

## Surface 3: Gemini Code Assist + Vertex AI

A different product on a different billing axis. Vertex AI hosts Gemini models under `aiplatform.googleapis.com` (not `generativelanguage.googleapis.com`). Quotas are per-region, per-model, and managed through GCP IAM and quota increase requests. Gemini Code Assist has its own per-user/per-org limits managed through Google Workspace or GCP project settings.

These surfaces are not claw-fleet's primary monitoring targets. See:
- [Vertex AI generative AI quotas](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/quotas)
- [Gemini Code Assist quotas](https://developers.google.com/gemini-code-assist/resources/quotas)
- [Gemini for Google Cloud quotas](https://docs.cloud.google.com/gemini/docs/quotas)

For Vertex AI quota monitoring, the service label is `aiplatform.googleapis.com` in your Cloud Monitoring filter.

---

## Subscription matrix

| Subscription | Consumer app access? | API access included? | API tier auto-bump? | Quota query path |
|---|---|---|---|---|
| Free (no account) | Yes (very limited) | No | N/A | N/A |
| AI Plus ($8/mo) | Yes, 2× limits | No | No | N/A |
| AI Pro ($19.99/mo) | Yes, 4× limits | No | No | N/A |
| AI Ultra $100/mo | Yes, 5× AI Pro | No | No | N/A |
| AI Ultra $200/mo | Yes, 20× AI Pro | No | No | N/A |
| API free tier (no billing) | No | Yes (free) | No | Cloud Monitoring (limited) |
| API Tier 1 (billing enabled) | No | Yes (paid) | Instant on billing | Cloud Monitoring + Cloud Quotas |
| API Tier 2 ($100+ spend) | No | Yes (higher limits) | After 3 days | Cloud Monitoring + Cloud Quotas |
| API Tier 3 ($1,000+ spend) | No | Yes (highest limits) | After 30 days | Cloud Monitoring + Cloud Quotas |

**Key takeaway:** "I pay $20/mo for AI Pro — do I get API access?" → No. You need a separate API key from AI Studio, with its own independent billing. An AI Pro subscription and an API key can coexist on the same Google account but do not share limits or billing.

---

## Monitoring — what claw-fleet can extract today

### Consumer plan (AI Pro / Ultra) — cookie liveness only

No usage numbers are queryable. The only signal available is whether the authenticated session is alive.

**Auth cookies required:**
```
__Secure-1PSID
__Secure-1PSIDCC
__Secure-1PSIDTS
NID
```

**Liveness check:** HTTP GET to `https://gemini.google.com/app` with the above cookies attached. A 200 with non-login HTML indicates a live session. A redirect to `accounts.google.com` indicates session expiry.

Note: `SAPISIDHASH` is used by other Google APIs (e.g., YouTube Data API calls from browser) but is not the primary session token for gemini.google.com liveness. The `__Secure-1PSID` family is the correct set for Gemini app sessions.

These cookies rotate frequently. Reverse-engineering the internal usage counter from the page DOM is fragile — Google moves these endpoints without notice and the counter is not exposed in the page source.

### API key holders — Cloud Monitoring queries

For any project with an API key and billing enabled, query quota consumption via Cloud Monitoring:

```bash
# List quota limits for the Gemini API service
gcloud services quotas list \
  --service=generativelanguage.googleapis.com \
  --project=YOUR_PROJECT_ID

# Get current quota usage (last 1 hour)
gcloud monitoring read \
  'metric.type="serviceruntime.googleapis.com/quota/rate/net_usage" AND resource.labels.service="generativelanguage.googleapis.com"' \
  --project=YOUR_PROJECT_ID \
  --freshness=1h
```

Free tier API keys have read access to their own quota metrics via `monitoring.timeSeries.list` with project-scoped credentials.

### ADC-equipped paid projects — timeSeries query

```python
from google.cloud import monitoring_v3
import time

client = monitoring_v3.MetricServiceClient()
project_name = f"projects/{PROJECT_ID}"

interval = monitoring_v3.TimeInterval()
now = time.time()
interval.end_time.seconds = int(now)
interval.start_time.seconds = int(now - 3600)  # last hour

results = client.list_time_series(
    request={
        "name": project_name,
        "filter": (
            'metric.type="serviceruntime.googleapis.com/quota/rate/net_usage"'
            ' resource.type="consumer_quota"'
            ' resource.labels.service="generativelanguage.googleapis.com"'
        ),
        "interval": interval,
        "view": monitoring_v3.ListTimeSeriesRequest.TimeSeriesView.FULL,
    }
)
```

The `quota_metric` label on each returned series identifies the specific limit (e.g., `generate_content_requests_per_minute`). The `value` gives tokens/requests consumed in that period.

---

## Known gotchas

1. **Multiple API keys on the same project do not stack quota.** Rate limits are per-project, not per-key. Adding a second API key to Project A does not double your RPM. To stack quota you need multiple GCP projects, each with its own API key and billing account. Source: [laozhang AI blog](https://blog.laozhang.ai/en/posts/gemini-api-free-tier).

2. **AI Pro does NOT lift API rate limits.** A Pro subscriber who also has an API key gets the same API quotas as a non-subscriber. The subscriptions are in separate billing systems. This confuses many developers who expect their $20/month to help with API quota.

3. **The 100-message/day limit is gone.** As of May 17, 2026, Google moved to compute-based limits that have no public fixed number. Some users reported the daily fixed cap silently dropped from 100 to ~25 prompts/day in the months before this change before being replaced entirely. If your monitoring code checks for a specific "N messages remaining" value, that data no longer exists.

4. **Free tier was cut 50–80% in December 2025.** Code or dashboards built before December 7, 2025 that assume the earlier free tier limits will be reading stale expectations. gemini-2.5-pro free tier RPD dropped to ~50–100 requests. Source: [aifreeapi.com](https://www.aifreeapi.com/en/posts/gemini-api-free-tier-rate-limits).

5. **gemini.google.com internal endpoints rotate.** Any reverse-engineering of the consumer app for usage counts will break without warning. Google has no obligation to maintain undocumented internal API stability. Treat consumer monitoring as liveness-only.

6. **Spend caps have a ~10-minute enforcement lag.** A burst request load can accrue charges beyond the cap during this window. This is documented by Google.

7. **Free tier API keys: Cloud Monitoring is available but limited.** Even without billing, a GCP project's quota metrics for `generativelanguage.googleapis.com` are accessible via `monitoring.timeSeries.list`. However, historical retention is shorter than for paid projects.

---

## Sources

- [Google One AI Plans pricing](https://one.google.com/about/google-ai-plans/)
- [Google I/O 2026 subscription announcement](https://blog.google/products-and-platforms/products/google-one/google-ai-subscriptions/)
- [Google AI Pro features (support doc)](https://support.google.com/googleone/answer/14534406)
- [Gemini app usage limits by tier (support doc)](https://support.google.com/gemini/answer/16275805)
- [ai.google.dev rate limits docs](https://ai.google.dev/gemini-api/docs/rate-limits)
- [ai.google.dev pricing docs](https://ai.google.dev/gemini-api/docs/pricing)
- [Google blog: Project Spend Caps announcement](https://blog.google/innovation-and-ai/technology/developers-tools/more-control-over-gemini-api-costs/)
- [Google blog: AI Pro benefits in AI Studio](https://blog.google/innovation-and-ai/technology/developers-tools/google-one-ai-studio/)
- [9to5Google: AI Pro/Ultra in AI Studio](https://9to5google.com/2026/04/20/google-ai-studio-limits/)
- [PCWorld: Gemini usage limit changes](https://www.pcworld.com/article/3142744/google-just-made-big-changes-to-gemini-usage-limits.html)
- [Android Authority: AI Pro downgrade](https://www.androidauthority.com/google-ai-pro-usage-limits-3669063/)
- [Android Central: AI Pro quiet downgrade](https://www.androidcentral.com/apps-software/ai/google-ai-pro-plan-just-got-a-quiet-downgrade)
- [Community thread: 100→25 prompt drop](https://support.google.com/gemini/thread/411457870)
- [Developer forum: quota exceeded 429 errors with metric names](https://discuss.ai.google.dev/t/quota-exceeded-for-metric-generativelanguage-googleapis-com-generate-content-free-tier-requests-limit-50/105796)
- [Developer forum: AI Pro + API access question](https://discuss.ai.google.dev/t/does-google-one-ai-pro-include-api-access-for-veo-3-or-is-it-limited-to-flow-only/110794)
- [GCP Cloud Monitoring quota metrics guide](https://docs.cloud.google.com/monitoring/alerts/using-quota-metrics)
- [GCP Cloud Monitoring API docs](https://docs.cloud.google.com/apis/docs/monitoring)
- [Vertex AI generative AI quotas](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/quotas)
- [Gemini Code Assist quotas](https://developers.google.com/gemini-code-assist/resources/quotas)
- [laozhang AI: multiple API keys do not stack quota](https://blog.laozhang.ai/en/posts/gemini-api-free-tier)
- [aifreeapi.com: December 2025 free tier cuts](https://www.aifreeapi.com/en/posts/gemini-api-free-tier-rate-limits)
- [Unofficial cookie-based Gemini API wrappers](https://github.com/HanaokaYuzu/Gemini-API)

---

## 2026-05-21 update: queryable surfaces and I/O 2026 findings

### What Google I/O 2026 (May 19-20) shipped for monitoring

Google I/O 2026 produced no new programmatic quota or usage endpoint for any Gemini surface. The developer keynote covered Managed Agents in the Gemini API, Gemini CLI `/stats model` session-scoped token counts, and native Android support in AI Studio. None of these expose a per-account quota consumption REST call.

What I/O did ship for consumer users: a **Usage Limits dashboard inside the Gemini app and Gemini Desktop**, visible under Settings & Help. It shows real-time compute consumption as a percentage, a countdown to the next 5-hour reset, and a weekly allowance tracker. This is a read-only UI element with no documented backing endpoint. Google confirmed the shift from fixed message counts to compute-based credits (free tier: 100 AI credits/month, AI Pro: 1,000/month, AI Ultra: 25,000/month). The old fixed-number daily cap (previously ~100 Gemini 2.5 Pro messages/day, quietly cut to ~25 before being dropped) is gone.

Source: [androidsage.com Gemini usage limits](https://www.androidsage.com/2026/05/19/geminis-new-usage-limits/), [Google I/O developer highlights](https://blog.google/innovation-and-ai/technology/developers-tools/google-io-2026-developer-highlights/), [Gemini Apps limits support doc](https://support.google.com/gemini/answer/16275805).

### Surfaces probed and verdict

**Surface 1: Consumer app (gemini.google.com Settings > Usage Limits)**

The usage dashboard exists and shows percentage consumed + reset time. Google does not expose its backing endpoint. The DOM does not surface structured data in a stable way: Google's internal RPC for the consumer app uses BrowserChannel/JSPB encoding that rotates without versioning commitments. Community reverse-engineering projects (e.g., `HanaokaYuzu/Gemini-API`) confirm the instability. No reliable scrape target exists. Verdict: liveness probe only, same as before.

**Surface 2: Gemini CLI `/stats model`**

Returns per-session token counts only. No cross-session or daily remaining quota. Official response in `google-gemini/gemini-cli` discussion #3096: "there's no way within Gemini CLI to see your daily quota — at least, not yet." The CLI uses `generativelanguage.googleapis.com/v1beta` and standard API key auth; no sidecar quota endpoint is called. Verdict: not usable for claw-fleet.

**Surface 3: AI Studio rate-limit dashboard (aistudio.google.com/rate-limit)**

The dashboard shows RPM/TPM/RPD per project for API-key holders. Google documents this URL in its rate limits docs but provides no REST equivalent. The page's network requests are not public. No API endpoint was shipped at I/O to replace or expose this dashboard. Verdict: UI-only, no polling path.

**Surface 4: Cloud Monitoring timeSeries query (most concrete queryable path for API-key accounts)**

This is the best existing option and nothing shipped at I/O that improves on it. The endpoint:

```
POST https://monitoring.googleapis.com/v3/projects/{PROJECT_ID}/timeSeries:query
```

Filter: `metric.type="serviceruntime.googleapis.com/quota/rate/net_usage" resource.labels.service="generativelanguage.googleapis.com"`

Auth: ADC (`gcloud auth application-default login`) or service account with `roles/monitoring.viewer`. Works for any project with billing enabled. Returns a time-series of quota consumption per metric label (e.g., `generate_content_free_tier_requests`). Refresh cadence: GCP Monitoring allows polling every 60 seconds with no documented quota concern at that rate on the Monitoring API itself (it has generous quota: 12,000 read requests/minute by default). Verdict: this is claw-fleet's best Gemini API path.

**Surface 5: Cloud Billing REST API (prepaid credit balance)**

`cloudbilling.googleapis.com/v1` provides `billingAccounts`, `billingAccounts.projects`, `projects`, `services`, and `services.skus` resources. None expose a "prepaid credits remaining" field. The AI Studio billing tab (`aistudio.google.com`) is the only place the prepaid balance is visible; Google explicitly states "All balance management and transaction history for the Gemini API must be done directly within the Google AI Studio Billing tab." Verdict: no polling path for prepaid balance.

**Surface 6: Google Account / One / myactivity.google.com**

`myactivity.google.com/product/gemini` lists individual prompts but does not expose a count or aggregate in a structured format. `one.google.com` and `myaccount.google.com` have no AI usage section as of May 2026. The AI credits counter (100/1,000/25,000 per month by tier) is only visible inside the Gemini app, not on the account dashboard. Verdict: nothing queryable.

### claw-fleet integration design for Surface 4 (Cloud Monitoring)

**Proposed second Gemini row: G2 = API quota (alongside G1 = consumer liveness)**

| Integration dimension | Detail |
|---|---|
| Endpoint | `POST monitoring.googleapis.com/v3/projects/{PROJECT_ID}/timeSeries:query` |
| Auth | ADC via `google-auth-library-python` or `gcloud`; service account JSON also works |
| Refresh cadence | Every 5 minutes is safe; Cloud Monitoring has its own 1-min resolution, so sub-5-min polling returns identical data and wastes quota |
| Fields to extract | `quota_metric` label (e.g., `generate_content_requests_per_minute`) + latest `value.int64Value` as consumed units; divide by known limit to get `utilization` 0-100 |
| Cache schema mapping | Map daily RPD metric to `usage.seven_day.utilization` slot (relabeled "daily" in widget display); map RPM metric to `usage.five_hour.utilization` slot (relabeled "per-min"); `resets_at` = midnight Pacific for RPD, rolling 60s for RPM |
| Absolute count | `utilization_pct * documented_limit / 100` — requires storing the documented free-tier limit in config (e.g., gemini-2.5-pro RPD=100); display as `73/100 req today` |
| Failure modes | ADC not configured (error on first call, surface in cache as `auth_error`); project has no billing (Cloud Monitoring returns empty time series for free-tier projects — catch empty result, fall back to "no data"); metric labels change between model generations (pin to known labels, log unknown ones) |

**Probe script structure (`src/bin/claw-fleet-gemini-api`):**

```
1. Source ~/.claude/vault.env for PROJECT_ID and optional service account path
2. Build GCP credentials (ADC or service account JSON)
3. POST to timeSeries:query with 1-hour interval
4. Parse response: extract metric label + latest int64 value per series
5. Load configured limits from ~/.config/claw-fleet/config.json (gemini_api.limits map)
6. Compute utilization = value / limit * 100 for each metric
7. Write to cache as accounts[].gemini_api with same schema as accounts[].usage
8. Handle empty results (free tier with no billing): write utilization=null, note="no_billing_data"
9. Handle auth errors: write utilization=null, note="auth_error"
```

This is ~40 lines in Python using `google-auth` + `requests`. The Monitoring API does not require `google-cloud-monitoring` SDK — a plain POST with a bearer token works.

### What a follow-up agent should dig into

1. Confirm whether the AI credits counter (100/1,000/25,000/month by tier) inside the Gemini app is accessible in any `WIZ_global_data` JavaScript variable on the `gemini.google.com/app` page. A headless Chrome fetch with valid session cookies targeting that variable would take ~10 minutes and would be definitive. If it is there, it's the most useful consumer signal available.
2. Check whether `generativelanguage.googleapis.com` added any `/v1beta/models/{model}/getQuota` or `/v1beta/usage` method post-I/O 2026. The API reference at `ai.google.dev/api` should be scanned for new endpoints added in May 2026.
3. Verify the exact Cloud Monitoring metric label names for Gemini 3.x models (the existing docs reference `generate_content_free_tier_requests` for Gemini 1.x/2.x — confirm whether Gemini 3 Pro/Flash use the same labels or new ones).

Sources: [Google I/O 2026 developer highlights](https://blog.google/innovation-and-ai/technology/developers-tools/google-io-2026-developer-highlights/), [androidsage Gemini usage limits](https://www.androidsage.com/2026/05/19/geminis-new-usage-limits/), [Gemini Apps limits support doc](https://support.google.com/gemini/answer/16275805), [gemini-cli discussion #3096](https://github.com/google-gemini/gemini-cli/discussions/3096), [gemini-cli discussion #4489](https://github.com/google-gemini/gemini-cli/discussions/4489), [Cloud Billing API reference](https://docs.cloud.google.com/billing/docs/reference/rest), [ai.google.dev billing docs](https://ai.google.dev/gemini-api/docs/billing), [Cloud Monitoring timeSeries:query](https://docs.cloud.google.com/monitoring/alerts/using-quota-metrics).

---

---

## 2026-05-21 update: G1 switched to API-key probe

The G1 row (`gemini-1`, `alisalaah@gmail.com`) previously used a cookie-based liveness probe against `gemini.google.com/app`. That approach required manually re-exporting session cookies on every rotation and provided only a binary alive/dead signal with no usage data.

G1 now probes the Gemini API directly using the `GEMINI_API_KEY_OPENCLAW` API key (openclaw-io GCP project, billing-attached Tier 1). The key validates with a lightweight `GET /v1beta/models?pageSize=1` call — no token consumption, no per-content quota burn.

### New vault vars

| Var | Required | Default | Purpose |
|---|---|---|---|
| `GEMINI_API_KEY_OPENCLAW` | Yes | — | API key for openclaw-io GCP project |
| `GEMINI_GCP_PROJECT_ID` | No | `openclaw-io` | GCP project ID for Cloud Monitoring quota queries |

### Two operating modes

**Mode A — API key only (no ADC):** The probe validates the key and writes `status=ok`, `quota_opaque=true`, all usage slots null. The note field carries the command to enable real numbers. This is the active mode as of 2026-05-21.

**Mode B — API key + ADC:** When `~/.config/gcloud/application_default_credentials.json` exists and `gcloud auth application-default print-access-token` returns a token, the probe additionally queries Cloud Monitoring `serviceruntime.googleapis.com/quota/rate/net_usage` for `generativelanguage.googleapis.com`. Per-minute quota consumption maps to `usage.five_hour`; per-day maps to `usage.seven_day`. `quota_opaque` becomes `false` on this path.

### Enabling real consumption numbers

Run once in any terminal:

```bash
gcloud auth application-default login --project=openclaw-io
```

The LaunchAgent (every 300s) will automatically pick up the ADC credentials on its next fire and switch to Mode B without any restart.

### Status codes

`ok` | `no_auth` (key missing) | `auth_invalid` (401/403 from API) | `api_error` (other HTTP) | `network_error`

The old `auth_expired` and `throttled` codes from the cookie probe are gone.

---

## Cloud Monitoring math

claw-fleet Mode B queries two Cloud Monitoring time series for `generativelanguage.googleapis.com` on the `openclaw-io` GCP project. This section documents the exact metric labels, window choices, limit resolution strategy, and known gaps.

### Metrics used

| Metric type | Purpose |
|---|---|
| `serviceruntime.googleapis.com/quota/rate/net_usage` | Per-minute consumption counters (one point per minute per series) |
| `serviceruntime.googleapis.com/quota/limit` | Quota limit gauge (one series per `quota_metric` label) |
| `serviceruntime.googleapis.com/quota/ratev2/limit` | Not queried — returns 0 series on `openclaw-io` Tier 1 paid |

### Series filtering

The `net_usage` query returns up to 6 series for this project. claw-fleet discards any series whose `quota_metric` label does not contain `generate_content_requests`. This filters out:

- `api_requests` — counts the poller's own `ListModels` probe calls
- `model_requests` — aggregate model-level counter that also includes `ListModels`

Only `generate_content_requests` series (tagged to `StreamGenerateContent` method) represent actual API content requests. The filter uses a substring check: `"generate_content" in qm`.

### Window choices

| Slot | Query window | Aggregate |
|---|---|---|
| `five_hour` | Last 86400s (24h) — full window; then filtered to last 300s in Python | Sum of point values where `endTime >= now - 300s` |
| `seven_day` | Same 24h query; sum all points without time filter | Rolling 24h total request count |

A single 24h query returns both. 300 seconds corresponds to 5 one-minute buckets, giving `sum_5m / 5 = avg_rpm`.

### Limit resolution

claw-fleet queries `quota/limit` over a 1-hour window. It iterates the returned series looking for a `quota_metric` label containing `model_requests` with a value that is both positive and below MaxInt64 (`9_223_372_036_854_775_807`). The `api_requests` series returns MaxInt64 (no enforced limit) and is skipped. The `model_requests` series returns 200 on Tier 1, which is used as the RPM denominator.

No published finite daily limit exists for `generate_content_requests` on Tier 1 paid projects. `seven_day.utilization` is therefore `null`; raw counts are always reported in the note string regardless.

### Utilization formulas

```
avg_rpm = sum_5m / 5.0
five_hour.utilization = min((avg_rpm / rpm_limit) * 100, 100.0)  # capped at 100%

seven_day.utilization = null  # RPD limit not published for Tier 1
```

If `sum_5m == 0`, `avg_rpm = 0.0` and `utilization = 0.0` (not `null`). The zero case is a real data point.

### Reset times

| Slot | Reset epoch formula |
|---|---|
| `five_hour.resets_at` | `(int(now) // 60 + 1) * 60` — next minute boundary (RPM window) |
| `seven_day.resets_at` | `(floor(now / 86400) + 1) * 86400` — next UTC midnight (RPD window) |

Both `resets_in` strings are human-readable: `Xs` for the RPM slot, `Xh YYm` for the RPD slot.

### Error handling

Any exception in the Monitoring block (`HTTPError`, `URLError`, `KeyError`, `ValueError`) sets `quota_opaque=True` and falls through to the standard Mode B partial entry. The error is logged to `~/.claude/claw-fleet-debug.log` with the exception message.

### GCP documentation references

- [Cloud Monitoring timeSeries API](https://cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.timeSeries/list)
- [Quota metrics for Service Usage](https://cloud.google.com/monitoring/api/metrics_gcp#serviceruntime)
- [Gemini API quota and limits](https://ai.google.dev/pricing)

---

## Last updated

2026-05-21
