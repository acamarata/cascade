# Getting an LLM up: Gemini Free Pool (and the local-LLM alternative)

This guide gets Cascade an LLM to work with **before** you wire in your paid/seat
models (Claude, Codex, OpenCode, etc.). The fastest, most capable near-term path
is the **Gemini Free Pool**: a set of free Google Gemini API keys that Cascade's
daemon rotates through, so the modest per-key free quota adds up to something
useful for real work.

There are two ways to build the pool — a **manual** path (fastest, safest) and an
**automated** path (the desktop app provisions GCP projects + keys for you). A
fully-offline **local LLM** option is covered at the end.

---

## How the pool works (so the setup makes sense)

- A free Gemini API key has a modest rate limit (roughly **15 requests/min and
  ~1,500 requests/day** on the flash models — *check current limits, Google
  changes them*).
- Each key is tied to one Google **Cloud project**. One Google account can hold
  several projects; you can also use several accounts you own.
- Cascade's daemon runs a **Gemini proxy** at `127.0.0.1:3761`. It holds all your
  enabled keys as a pool, **round-robins** requests across them, and when a key
  returns HTTP 429 (rate limited) it puts that key on a short **cooldown** and
  immediately retries with the next key.
- Net effect: **N keys ≈ N× effective throughput**. Four keys turns ~15 rpm into
  ~60 rpm — enough for Cascade to draft, summarize, triage, and assist while your
  premium models are offline.

```
your agents/tools ──▶ http://127.0.0.1:3761  ──▶ key1 ─┐
                       (Cascade Gemini proxy)    key2 ─┤ round-robin,
                                                 key3 ─┤ 429 → cooldown → next
                                                 keyN ─┘ ──▶ generativelanguage.googleapis.com
```

---

## ⚠️ One honest caveat on Terms of Service

Google's free tier is rate-limited **per project/account**. Pooling a few keys
from accounts and projects **you legitimately own** is reasonable personal use.
**Creating many throwaway Google accounts to evade the free-tier limits violates
Google's Terms** and risks all of them being banned. Cascade ships a conservative
default and even warns in-code before bulk key creation. Recommendation: **2–4
keys from accounts you actually own.** If you need more throughput than that,
move to a paid key (still works in the same pool).

---

## Path A — Manual (fastest, recommended to start)

No billing, no GCP console. Each key takes ~1 minute.

**For each key you want in the pool:**

1. Sign in to **Google AI Studio**: https://aistudio.google.com (use one of your
   own Google accounts).
2. Click **Get API key → Create API key**. AI Studio creates/uses a Cloud project
   automatically and gives you a key starting `AIza...`. Copy it.
3. Add it to Cascade's pool:
   ```bash
   cascade provider add --kind gemini --api-key AIza...your-key...
   ```
   The key is validated, then stored in your OS keychain (never written to disk
   in plaintext) and registered in the pool.
4. Repeat steps 1–3 for each additional account/key (2–4 total recommended).

**Verify the pool:**
```bash
cascade provider list          # should list each gemini key/slot
```

That's it — the daemon's proxy picks the keys up automatically and starts
rotating. Skip to **"Pointing tools at the pool"** below.

---

## Path B — Automated (desktop app provisions for you)

The Cascade desktop app can run the whole **Google → GCP project → enable
Generative Language API → create key** flow for you, repeated into a pool. Use
this when you'd rather not click through AI Studio N times.

1. Launch the Cascade app and open onboarding (or **Settings → Providers →
   Gemini Free Pool**).
2. Choose **Connect Google** and sign in with OAuth. Cascade receives a scoped
   access token (never logged, never stored in plaintext).
3. Choose **Full-Auto provision** and how many keys to create. Cascade will, per
   project: create a GCP project → enable the `generativelanguage` API → mint an
   API key → add it to the pool. It polls each project to activation (up to ~30s)
   and writes a **resumable checkpoint**, so if it's interrupted you can re-run
   and it skips projects it already created (idempotent).
4. When it finishes you'll see the pool populated with each `api_key + project_id`.

> Full-Auto requires the Google account to allow project creation. If your
> account is managed (Workspace org policy) and blocks project creation, use
> Path A instead.

---

## Pointing tools at the pool

Cascade's own agents use the pool automatically once keys are registered. To send
**external** OpenAI/Gemini-compatible tools through it, point them at the local
proxy instead of Google directly:

- **Base URL:** `http://127.0.0.1:3761`
- **Upstream it fronts:** `https://generativelanguage.googleapis.com`
- No API key needed from the client — the proxy injects a pooled key per request.

So any tool that lets you set a custom Gemini/Generative-Language base URL can ride
the pool and get the multiplied quota + automatic failover.

---

## Local LLM alternative (fully offline, opt-in)

If you want **zero external calls** — air-gapped, no quotas, no ToS questions —
Cascade can run a small model on-device (gemma-2-2b, CPU; Metal-accelerated on
Apple Silicon).

- It's **opt-in**: the default build does not include it (it pulls a heavy ML
  stack that doesn't build cleanly on every platform yet). Build/install with the
  `local-llm` feature enabled, then:
  ```bash
  cascade models list             # see available local models
  cascade models download gemma-2-2b
  ```
  The daemon registers any model under `~/.cascade/models/` as a `local:` provider.
- **Trade-off:** fully private and free, but much weaker and slower than Gemini.
  Good for offline drafting/triage; not for heavy reasoning.

**Which to use:** start with the **Gemini Free Pool** (capable, fast, trivial to
set up). Keep **local-llm** as an offline fallback. As your premium models come
online (Claude, Codex, OpenCode), Cascade's router will prefer them and the pool
becomes the free fallback tier.

---

## Quick reference

| Action | Command |
|---|---|
| Add a Gemini key to the pool | `cascade provider add --kind gemini --api-key AIza...` |
| List pooled providers/keys | `cascade provider list` |
| Proxy endpoint (point tools here) | `http://127.0.0.1:3761` |
| Local model list / download (opt-in) | `cascade models list` · `cascade models download <id>` |

- Free key source: https://aistudio.google.com → Get API key
- Recommended pool size: **2–4 keys from accounts you own**
- Keys are stored in the OS keychain, never plaintext on disk.
