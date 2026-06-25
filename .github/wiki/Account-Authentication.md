# Account Authentication & Token Refresh

How Cascade gets live usage data for each account in the fleet widget, how tokens
refresh automatically, and how to re-authenticate when a credential expires.

The fleet widget shows one row per account. A row reads `re-auth` (amber) when its
credential is dead — that is an **authentication** problem, not a Cascade bug. This
page explains how to fix each one.

---

## How each provider is polled

| Provider | Row | Auth method | Live numbers? |
|---|---|---|---|
| Anthropic (Claude Max) | `A1`, `A2`, … | OAuth access token → `GET api.anthropic.com/api/oauth/usage` | Yes (5h + weekly Opus/Sonnet) |
| OpenAI (Codex) | `Co`, `C1`, … | `codex` CLI session | Yes |
| OpenCode (Go + Zen) | `OC` | `opencode` CLI / API | Yes |
| Google (Gemini Pro/Ultra) | `Ge`, `G1`, … | API key + (optional) Cloud Monitoring via gcloud ADC | Only with gcloud ADC; otherwise quota-opaque |
| GFP (Gemini Flash Free Pool) | `GP` | Round-robin API-key pool | Shows key count (`N keys free`) |

The daemon's fleet poller refreshes every ~5 minutes and merges results into
`~/.cascade/accounts/quota.json`, which the widget reads.

---

## Anthropic — automatic OAuth token refresh

Cascade refreshes Claude access tokens **automatically**. Before each poll it:

1. Reads the account's OAuth credential from the macOS keychain
   (`Claude Code-credentials[-<hash>]`, one per account config dir).
2. If the access token is expired, POSTs the stored **refresh token** to the OAuth
   token endpoint:
   - URL: `https://platform.claude.com/v1/oauth/token`
   - Body: `{"grant_type":"refresh_token","refresh_token":"<rt>","client_id":"9d1c250a-e61b-44d9-88ed-5944d1962f5e"}`
   - `client_id` is the public Claude Code OAuth client.
3. Writes the rotated `access_token` / `refresh_token` / `expiresAt` back to the same
   keychain entry, so subsequent polls stay authenticated with **no manual step**.

This means: **once an account is logged in, it stays live indefinitely.**

### When you DO need to act: expired refresh token

Refresh tokens themselves eventually expire (or are revoked if you log in elsewhere).
When that happens the endpoint returns `invalid_grant: "Refresh token expired"`, the
row shows `re-auth`, and **only one interactive login fixes it** (no program can
refresh an expired refresh token — that is by design).

**One click (recommended):** click the amber **Click here to re-auth** on that row in
the widget. Cascade opens a Terminal running `~/.cascade/bin/cascade-reauth <account>`,
which runs `claude auth login` scoped to that account's config dir — your browser opens,
`claude` runs its own localhost OAuth callback (the full handshake, no code-pasting),
writes the rotated token to the keychain, and the helper immediately re-polls so the row
fills within seconds.

**Manual equivalent:**

```sh
# A1 = ~/.claude-acc1, A2 = ~/.claude-acc2, …
~/.cascade/bin/cascade-reauth claude-acc2
# or directly:
CLAUDE_CONFIG_DIR=~/.claude-acc2 claude auth login --claudeai
```

From then on, automatic refresh (above) keeps the row live indefinitely — re-auth is a
one-time fix per expired refresh token.

> Account → row mapping: accounts are discovered from `~/.claude-acc*` dirs (sorted)
> plus the primary `~/.claude`. Labels are `A1, A2, …` in that order.

---

## Google Gemini — subscription quota via Antigravity (the `Ge` row)

The `Ge` row tracks your **Gemini Pro/Ultra ($20/mo+) subscription**, not an API key.
That subscription's usage is **not** exposed by a Gemini API key (API keys are the
separate, metered developer tier). It IS exposed through the same **Cloud Code Assist**
backend the Antigravity IDE uses — so Cascade reads it the same way the IDE does.

**Sign in (one-time):** click **Click here to re-auth** on the `Ge` row, or run:

```sh
~/.cascade/bin/cascade-agy-auth      # opens the browser, OAuth + localhost callback
```

This stores a refresh token at `~/.cascade/agy-token.json`. The poller then runs
`~/.cascade/bin/cascade-agy` every cycle, which:

1. refreshes the access token (`oauth2.googleapis.com/token`),
2. `POST cloudcode-pa.googleapis.com/v1internal:loadCodeAssist` → your project id,
3. `POST …/v1internal:fetchAvailableModels` → `models[*].quotaInfo.remainingFraction`,

and writes `SES = Gemini-3-Pro` usage, `WK = Gemini-3-Flash` usage (utilization =
`(1 − remainingFraction) × 100`, with the model's reset time). Keys/tokens are never
logged. Multiple Google accounts can be added (each `cascade-agy-auth` run appends).

> **Legacy API-key path** (`GEMINI_API_KEY_OPENCLAW` + optional gcloud ADC) still exists
> in `cascade-gemini` for tracking a *metered API project*, but it is a different product
> from the consumer subscription and is not what the `Ge` row uses by default.

---

## OpenAI Codex / OpenCode

These authenticate through their own CLIs — make sure the CLI is installed and logged
in, and on `PATH` for the poller (the poller LaunchAgent sets
`PATH=/opt/homebrew/bin:~/bin:/usr/bin:/bin`).

```sh
codex      # must be logged in
opencode   # must be logged in
```

---

## GFP (Gemini Flash Free Pool)

The `GP` row shows the number of round-robin API keys in the pool (`N keys free`) —
its available free-tier capacity. Keys are counted, **never logged**. Add keys via
`cascade accounts add` (or by extending the pool config); the count updates on the
next poll.

---

## The polling pipeline (for maintainers)

```
poller (LaunchAgent dev.cascade.poller, every 5 min)
  └─ src/bin/cascade  → refreshes Claude OAuth tokens, queries each provider
       └─ writes ~/.claude/usage-cache.json
            └─ cascaded daemon (fleet poller, ~60s) merges by account/provider
                 └─ writes ~/.cascade/accounts/quota.json
                      └─ CascadeApp widget reads + renders (refreshes every 30s)
```

A `re-auth` row means step 1 failed for that account (expired token / invalid key).
Everything downstream is automatic and self-healing once the credential is valid.
