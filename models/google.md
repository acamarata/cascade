# Google / Gemini — Deep Detail

Verified July 2026. See `models/README.md` for the master matrix and the GFP-backbone verdict; see `models/models.yaml` for the machine-readable entries.

Cascade accounts: **agy** (paid Google AI Pro/Ultra via cloudcode-pa), **GFP** (28-project free-tier key pool, `GEMINI_FREE_KEY_*` in `~/.claude/vault.env`). Dispatch: `agy` CLI for paid Gemini; GP proxy `:3761` (native Gemini format) / `:3762` (Anthropic-compat adapter) for the GFP free pool.

---

## 1. Paid / subscription tier (Google AI Pro/Ultra)

### Naming reality check [Certain]

The lineup is **not** "3.5 Pro vs 3.1 Pro" as parallel current options — it's sequential/staggered:

- **Gemini 3.1 Pro** — current shipped Pro model (preview status per ai.google.dev models page), "advanced intelligence, complex problem-solving, powerful agentic and vibe coding." No free tier; paid-only. [Certain]
- **Gemini 3.5 Pro** — announced at I/O (2026-05-19), targeted for June GA, **slipped to July 2026**, still in limited Vertex AI enterprise preview as of late June 2026. **NOT confirmed publicly released as of this research** — treat "is it out" as **not yet GA**, possibly landing within days of "now" (2026-07-06) but unverifiable live. [Likely, borderline Guessing on exact GA date — multiple blog/news sites cite a "July 17" leak, none official]
- **Gemini 3.5 Flash** — already GA (shipped 2026-05-19), and per DeepMind/Appwrite/llm-stats coverage, **Gemini 3.5 Flash outperforms Gemini 3.1 Pro on coding and agentic benchmarks** at a fraction of the price ($1.50/$9.00 per M vs 3.1 Pro's $2-4/$12-18 per M). This is the notable "best at" claim circulating — it's Flash beating the *previous-gen* Pro, not a 3.5-Pro-is-best claim. [Likely]
- No standalone "Gemini Ultra" **model** exists — "Ultra" is a **subscription plan** name (Google AI Ultra, $99.99/mo after a price cut from $249.99), not a distinct model ID. [Certain]

### Gemini 3.5 Pro specifics [Likely]

- **2,000,000 token context window** — largest of any frontier model as of the announcement, cited consistently across sources.
- **Deep Think** reasoning mode (Google's extended-thinking equivalent), targeting gold-medal-level math/programming olympiad performance; prior Gemini 3 Deep Think scored 84.6% on ARC-AGI-2.
- No official model card, pricing, or benchmark numbers published yet for 3.5 Pro specifically — Google has been "tight-lipped." [Certain — absence of data confirmed by fetch]
- Best-for (expected, not yet benchmark-confirmed): frontier reasoning + full-codebase/large-corpus ingestion (2M ctx) + agentic coding, positioned above 3.5 Flash. [Guessing]

### Access via Google AI Pro/Ultra subscription [Likely]

- **Google AI Plus**: $7.99/mo
- **Google AI Pro**: $19.99/mo — gives access to Gemini 3.1 Pro + higher Gemini-app usage limits
- **Google AI Ultra**: $99.99/mo (cut from $249.99 at I/O 2026) — highest limits, first access to Deep Think / 3.5 Pro preview features
- Gemini 3.5 Flash in the Gemini app gets "5x higher usage limits" than what Pro-tier plan users get for Pro models — Google is clearly steering high-volume usage toward Flash even for paying subscribers.
- Consumer access to 3.5 Pro at GA is expected to route through these same paid plans (Pro ~$20/mo, Ultra ~$99.99-250/mo), not a separate product.

---

## 2. Free tier ("GFP" pool candidates)

### Which models are free right now [Certain, per official ai.google.dev/gemini-api/docs/pricing fetch]

| Model | Free tier? | Paid price (in/out per 1M) | Status |
|---|---|---|---|
| **Gemini 3.5 Flash** | **Yes** | $1.50 / $9.00 | Stable, GA |
| **Gemini 3.1 Flash-Lite** | **Yes** | $0.25 / $1.50 | Stable |
| **Gemini 3 Flash** (preview) | Yes | $0.50 / $3.00 | Preview |
| **Gemini 2.5 Pro** | Yes (still) | $1.25-2.50 / $10-15 | Stable |
| **Gemini 2.5 Flash** | Yes | $0.30 / $2.50 | Stable |
| **Gemini 2.5 Flash-Lite** | Yes | $0.10 / $0.40 | Stable |
| **Gemini 2.0 Flash** | Was free | $0.10 / $0.40 | **Deprecated / shut down June 1, 2026** — dead, remove from any rotation |
| **Gemini 3.1 Pro** | **No** | $2-4 / $12-18 | Preview, paid-only |
| **Gemini 3.5 Pro** | No (not GA) | unpublished | Not yet released |

**Important conflict in the evidence:** the official ai.google.dev pricing page (fetched live) still shows a Free Tier column marked available for 2.5 Pro, contradicting some aggregator claims (apiyi.com, aifreeapi.com) that "Pro models were pulled from free tier April 1, 2026." Possible explanations: the April tightening applied to 3.x Pro specifically (3.1 Pro, 3.5 Pro) while grandfathering 2.5 Pro, or the aggregator articles are stale/wrong. **[Guessing which is correct — flag as unresolved, reverify live before depending on 2.5 Pro being free.]**

### Free tier rate limits — numbers are inconsistent across sources [Guessing on exact current numbers, Certain on structure]

Three different aggregators gave three different numeric tables for the same models (2.5 Flash: 15 RPM/1500 RPD/1M TPM vs 10 RPM/250 RPD/250K TPM in different sources). **The official ai.google.dev/gemini-api/docs/rate-limits page does NOT publish exact numbers anymore** — it explicitly punts to a live per-project dashboard: `aistudio.google.com/rate-limit`. This is itself a meaningful finding: Google stopped publishing a static table, meaning numbers are dynamic per-project/per-tier and **any number found in a blog post could be stale the day it's read.**

Directionally consistent across all sources:

- Free tier RPD is in the **low hundreds to ~1,500/day range per project**, not "unlimited."
- Free tier RPM is **low single-to-low-double digits** (roughly 5-30 RPM depending on model, Flash-Lite gets ~2x the RPM of Flash).
- TPM is comparatively generous (250K-1M range).
- **Rate limits are enforced per-project, not per-API-key.** Multiple keys inside the same project add zero quota. [Certain — corroborated by 3 independent sources plus consistent with official billing-account-level language]

### The multi-project strategy — critical finding [Certain on ToS existence, Guessing on enforcement risk]

- Rate limits are tracked **per Google Cloud project**. Creating separate projects (which is what the GFP 28-key/28-project rotation implies) **does** get independent quota pools per project — that mechanism is real and is exactly why key/project rotation "works" technically.
- **However: multiple sources explicitly state Google's terms of service prohibit creating multiple projects specifically to circumvent rate limits.** Enforcement is described as inconsistent/scale-dependent ("2-3 keys for dev testing is tolerated; dozens of projects for production-scale free usage risks account suspension"), but the practice is a documented ToS gray/red zone, not a sanctioned pattern. [Likely — three separate aggregator sources converge on this, though none quote exact ToS clause text]
- **Billing-account-level consolidation (effective March 23, 2026 per official docs)**: "All projects linked to a Cloud Billing account inherit the billing account's usage tier." If any of the 28 GFP projects ever get linked to a shared billing account (even accidentally, e.g. by adding a payment method to one for a different reason), that project's quota flips to shared/billing-account-level accounting — worth auditing that none of the 28 are billing-linked.
- No official statement found on whether **unlinked, no-billing** projects are pooled at the Google-*account* level (as opposed to Cloud *project* level) — the official billing doc is explicitly silent here. If the 28 projects are all under one Google account (not verified in this research), there's a non-zero chance Google could evolve toward account-level pooling given the April 2026 tightening trend — this is the single biggest structural risk to the "unlimited backbone" assumption. [Guessing]

---

## 3. Strategy verdict: free Flash pool as an always-on Haiku-substitute for grunt work

See `models/README.md` § GFP Backbone for the condensed verdict. Full reasoning:

### Quality vs Haiku [Likely, not independently benchmarked here]

- Gemini 3.5 Flash is reported (by DeepMind/Appwrite/llm-stats coverage) to **beat Gemini 3.1 Pro** (the prior-gen full Pro model) on coding/agentic benchmarks — a strong quality signal, well above what you'd expect from a "Haiku-tier" cheap model. If that holds, 3.5 Flash for classification/summarization/post-processing grunt work is very likely **quality-equal-or-better than Claude Haiku**, not just an acceptable substitute.
- Caveat: no independent third-party eval (lmarena, independent benchmark suite) was found comparing 3.5 Flash directly against Haiku 4.5 specifically — the comparison above is Flash-vs-its-own-sibling-Pro, not Flash-vs-Claude. Treat "quality good enough" as [Likely] based on strong proxy evidence, not [Certain].
- For truly trivial grunt work (triage, classify, grep+summarize) — the exact GCI-defined GP use case — even a materially weaker-than-Haiku model would suffice; the bar for this workload is low, so quality risk here is low regardless.

### Real constraints — this is where the "effectively unlimited" framing breaks down [Certain on structure, Guessing on exact thresholds]

1. **Per-project daily/minute caps are real and low relative to "unlimited"** — hundreds to ~1,500 RPD, single-to-low-double-digit RPM per project. A single project alone would throttle a busy Cascade session fast. The 28-project rotation is *precisely* the workaround for this — and it's a workaround for a known, documented cap, not evidence the cap doesn't exist.
2. **ToS risk is non-zero and directionally worsening.** Google tightened billing/tier policy twice in the last ~4 months (March 23 prepay/postpay change, April 1 Pro-model free-tier restriction). The trend line is "free tier gets stingier and more surveilled over time," not stable. A 28-project rotation built today could face new pooling rules with no notice — this already happened once this year for a different axis (billing-account consolidation).
3. **Latency**: not measured directly in this research; no official SLA difference found between free and paid tier latency, but free tier is documented as lower-priority-queued in some third-party commentary (unverified — [Guessing]).
4. **2.0 Flash is dead (June 1, 2026 shutdown)** — if any of the 28-key infrastructure still targets 2.0 Flash model IDs, that's a live outage waiting to surface, not a future risk. **Audit the GP proxy routing tables for this immediately.**
5. **No official confirmation that unlinked multi-project free quotas won't eventually get pooled per-Google-account** — this is the single largest unknown threatening the whole architecture's long-term viability.

### Verdict

**Yes, technically capable and currently viable** — Gemini 3.5 Flash / 3.1 Flash-Lite quality looks good enough (likely on par with or better than Haiku for classify/summarize/grunt work), and per-project quota rotation across 28 projects genuinely multiplies effective free throughput today. **But call it "high-volume-cheap," not "unlimited."** Two concrete risks to actively monitor, not assume away:

- **ToS exposure**: this is explicitly the pattern ("creating multiple projects to circumvent rate limits") that Google's documented policy calls out — low enforcement risk at hobbyist/personal scale, but not zero, and worth not treating as a permanent architectural bedrock.
- **Policy-drift risk**: two tightening events in 4 months (Mar 23 + Apr 1, 2026) means the free-tier rules the pool depends on are actively being narrowed. Build a fallback path (paid Flash-Lite at $0.10-0.25/M input is cheap enough to be a trivial paid fallback) rather than architecting as if the free pool is guaranteed stable.
- Recommend re-verifying the actual per-project RPM/RPD live at `aistudio.google.com/rate-limit` for a real project rather than trusting any published table — Google itself stopped publishing static numbers, which is a signal they change more often than docs get updated.

### Open questions

1. Are any of the 28 GP projects linked to a shared Cloud Billing account (even for unrelated reasons)? Worth auditing — that would collapse their quota into one shared pool per the March 2026 billing-account-level policy.
2. Should a live `aistudio.google.com/rate-limit` check be run against one of the 28 actual projects to get real current numbers instead of relying on conflicting third-party blog tables?
3. Should the GP proxy add a paid-Flash-Lite fallback path (cheap, ~$0.10-0.25/M) for when free-tier daily caps are hit, to reduce reliance on the multi-project rotation as sole capacity?

---

## Sources

- https://ai.google.dev/gemini-api/docs/pricing
- https://ai.google.dev/gemini-api/docs/rate-limits
- https://ai.google.dev/gemini-api/docs/billing
- https://blog.google/innovation-and-ai/models-and-research/gemini-models/gemini-3-5/ (Gemini 3.5 launch)
- https://blog.getbind.co/gemini-3-5-pro-slips-to-july-and-four-senior-google-researchers-just-left-for-anthropic/
- https://www.techtimes.com/articles/319318/20260629/gemini-35-pro-cleared-july-launch-fable-5-nears-return-gpt-56-stays-locked.htm
- https://deepmind.google/models/model-cards/gemini-3-5-flash/
- https://deepmind.google/models/model-cards/gemini-3-1-pro/
- https://appwrite.io/blog/post/gemini-3-5-flash-deep-dive
- https://gemini.google/subscriptions/
- https://tokenmix.ai/blog/gemini-api-free-tier-limits
- https://www.aifreeapi.com/en/posts/gemini-api-free-tier-complete-guide
- https://help.apiyi.com/en/google-gemini-api-free-tier-changes-april-2026-guide-en.html
- https://blog.laozhang.ai/en/posts/gemini-api-free-tier
- https://blog.laozhang.ai/en/posts/google-gemini-billing-tier-policy-changes
