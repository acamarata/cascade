# Architecture

## Cache Pipeline

```
 ┌──────────────────────────────────────────────────────────────┐
 │  LaunchAgents (staggered intervals)                          │
 │                                                              │
 │  io.clawfleet          (60s)   → claw-fleet          [A1-A4] │
 │  io.clawfleet.refresh  (wrap)  → claw-fleet-refresh  [A1-A4] │
 │  io.clawfleet.codex    (5min)  → claw-fleet-codex    [C1]    │
 │  io.clawfleet.gemini   (5min)  → claw-fleet-gemini   [G1]    │
 │  io.clawfleet.opencode (5min)  → claw-fleet-opencode [O1]    │
 │  io.clawfleet.watchdog (60s)   → claw-fleet-watchdog         │
 │  io.clawfleet.sanity   (3600s) → claw-fleet-sanity           │
 └───────────────────────────────┬──────────────────────────────┘
                                 │ each probe merges its results
                                 ▼
 ~/.claude/usage-cache.json   (source of truth)
 /tmp/claw-fleet-result.json  (back-compat copy)
                 │
      ┌──────────┼──────────┬──────────────┐
      ▼          ▼          ▼              ▼
  /aa CLI   Übersicht   SwiftBar      Other consumers
 (terminal) (desktop)  (menu-bar)    (CRD, scripts)
```

All consumers are read-only. Only the probe scripts write the cache. Each probe preserves unknown top-level keys on merge, so probes run independently without overwriting each other's data.

## Providers and probe methods

| Provider | Accounts | Auth | Probe method |
|---|---|---|---|
| Claude (Anthropic) | A1–A4 | OAuth via macOS Keychain | `GET /api/oauth/usage` with `anthropic-beta: oauth-2025-04-20` |
| Codex (OpenAI ChatGPT) | C1 | OAuth via macOS Keychain | `POST /backend-api/codex/responses` probe; reads `x-codex-*` response headers |
| Gemini API | G1 | API key (`GEMINI_API_KEY_OPENCLAW`) or ADC | Mode A: Gemini API. Mode B: Cloud Monitoring `timeSeries:query` on `serviceruntime.googleapis.com` |
| OpenCode Go | O1 | Session cookie (`OPENCODE_GO_AUTH_COOKIE`) | HTML scrape of `opencode.ai/workspace/<id>/go` |

## Account labels

Every row in the widget and SwiftBar menu starts with a one-letter provider prefix followed by a sequential index.

| Provider | Prefix | Example labels |
|---|---|---|
| Anthropic (Claude Code) | `A` | `A1`, `A2`, `A3`, `A4` |
| OpenAI Codex | `C` | `C1` |
| Gemini | `G` | `G1` |
| OpenCode Go | `O` | `O1` |

## Data Flow Details

1. **Account detection**: `claw-fleet` globs `~/.claude-acc*` (skipping `.bak*` suffixes). If `~/.config/claw-fleet/config.json` exists, it overrides the glob with an explicit list. `claw-fleet-codex` globs `~/.codex*` the same way.

2. **Keychain lookup (Claude + Codex)**: For each config dir, the service name is `Claude Code-credentials-<sha256(abs_path)[:8]>`. This matches Claude Code's own keychain naming. The raw JSON payload is `{"claudeAiOauth": {"accessToken": "...", "refreshToken": "...", "expiresAt": <ms>, ...}}`.

3. **Email extraction**: `claw-fleet` decodes the JWT access token and looks for `email` or a `sub` claim containing `@`. Fallback is the config dir basename.

4. **Token refresh**: If `expiresAt` is within 60 seconds of expiry, `claw-fleet` runs `CLAUDE_CONFIG_DIR=<dir> claude -p "ok" --max-turns 1` to trigger Claude Code's internal OAuth refresh, then re-reads the keychain.

5. **Merge logic**: On API failure for an account, the last known good data is preserved from the prior cache. `last_pull_at` is only updated on a successful pull. A `last_error` field records what failed.

## Reliability

**Back-off**: when a probe receives `rate_limit_error` for an account, it records `back_off_until` (epoch seconds) using exponential backoff starting at 2 minutes and capping at 10 minutes. While an account is in active back-off, the probe skips it and the widget suppresses the stale `*` marker for that account (the marker would be misleading — it is a deliberate pause, not a failure).

**Watchdog** (`claw-fleet-watchdog`, 60s): encoding-safe pgrep checks that the probe daemons are running, truncates logs that exceed size limits, and triggers a widget self-heal if Übersicht stops refreshing.

**Sanity check** (`claw-fleet-sanity`, 3600s): deep validation of the cache. Writes a top-level `sanity` block with `ok`, `checked_at`, and an `anomalies` array. Flags accounts where `resets_at` is in the past (which suggests a window definition has changed upstream) or where utilization values are outside sane bounds. Run `~/bin/claw-fleet-sanity` manually to check the current cache state.

**Stale threshold**: widgets mark an account with `*` (Übersicht) or `⚠` (SwiftBar) when `now - last_pull_at > 1800` seconds (30 minutes). Accounts in active back-off are excluded from this check.

## Cache Schema

```json
{
  "queried_at": 1714000000,
  "sanity": {
    "ok": true,
    "checked_at": 1714003600,
    "anomalies": []
  },
  "accounts": [
    {
      "account": "claude-acc1",
      "email": "user@example.com",
      "provider": "anthropic",
      "label": "A1",
      "queried_at": 1714000000,
      "last_pull_at": 1714000000,
      "last_api_attempt": 1714000000,
      "back_off_until": null,
      "error": null,
      "usage": {
        "five_hour": {
          "utilization": 42.5,
          "resets_at": 1714003600,
          "resets_in": "1h 00m"
        },
        "seven_day": {
          "utilization": 61.0,
          "resets_at": 1714518400,
          "resets_in": "143h 33m"
        },
        "seven_day_sonnet": {
          "utilization": 55.0,
          "resets_at": 1714518400,
          "resets_in": "143h 33m"
        },
        "seven_day_opus": {
          "utilization": 10.0,
          "resets_at": 1714518400,
          "resets_in": "143h 33m"
        },
        "extra_usage": {
          "is_enabled": true,
          "monthly_limit": 1000000,
          "used_credits": 250000
        }
      }
    }
  ]
}
```

## Component Map

| Component | Location | Purpose |
|---|---|---|
| `src/bin/claw-fleet` | `~/bin/claw-fleet` | Claude account discovery, keychain read, OAuth probe, cache write |
| `src/bin/claw-fleet-refresh` | `~/bin/claw-fleet-refresh` | Wrapper: runs claw-fleet + writes markdown summary |
| `src/bin/claw-fleet-codex` | `~/bin/claw-fleet-codex` | Codex account discovery, OAuth probe, header extraction, cache merge |
| `src/bin/claw-fleet-gemini` | `~/bin/claw-fleet-gemini` | Gemini API key + optional ADC Cloud Monitoring probe, cache merge |
| `src/bin/claw-fleet-opencode` | `~/bin/claw-fleet-opencode` | OpenCode Go HTML scrape with session cookie, cache merge |
| `src/bin/claw-fleet-watchdog` | `~/bin/claw-fleet-watchdog` | Encoding-safe pgrep, log truncation, widget self-heal |
| `src/bin/claw-fleet-sanity` | `~/bin/claw-fleet-sanity` | Deep cache validation, reset-anomaly detection |
| `src/widgets/ubersicht/claw-fleet.widget/index.jsx` | `~/Library/Application Support/Übersicht/widgets/` | Desktop widget, reads cache, drag/snap UI |
| `src/widgets/swiftbar/claw-fleet.5s.sh` | `~/Library/Application Support/SwiftBar/Plugins/` | Menu-bar plugin, reads cache |
| `src/widgets/macos-native/` | (repo only, not installed) | macOS native widget scaffold — not yet built |
| `src/launchd/` | `~/Library/LaunchAgents/` (rendered) | 7 plist templates for the LaunchAgents above |
| `~/.config/claw-fleet/config.json` | (optional, user-created) | Overrides account auto-detection |
| `~/.config/claw-fleet/widget-position.json` | (auto-written by drag) | Persists Übersicht widget position |
