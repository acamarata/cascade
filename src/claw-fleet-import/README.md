# Claw Fleet

A macOS dashboard for tracking usage across multiple AI coding accounts — Claude (A1–A4), Codex (C1), Gemini API (G1), and OpenCode Go (O1).

Probe scripts write to a shared local cache on a tiered schedule (Claude every 60s, others every 5min, sanity check hourly). Two surfaces read that cache with no additional API calls:

- **Desktop widget** (Übersicht, 345px, draggable, auto-refresh every 5s)
- **Menu-bar dropdown** (SwiftBar, per-account submenus)

A macOS native widget scaffold lives at `src/widgets/macos-native/` but is not yet built. See its README for planned build steps.

---

## What it monitors

| Provider | Accounts | Probe interval | What you get |
|---|---|---|---|
| Claude (Anthropic) | A1–A4 | 60s | 5h + weekly utilization, model sub-buckets, extra-credit balance |
| Codex (OpenAI ChatGPT) | C1 | 5min | 5h + weekly windows via `x-codex-*` headers |
| Gemini API | G1 | 5min | Cloud Monitoring quota metrics (requires ADC or API key) |
| OpenCode Go | O1 | 5min | Per-window utilization via workspace HTML scrape (session cookie) |

All four probes feed `~/.claude/usage-cache.json`. The widget and SwiftBar read that file.

---

## Install

### Prerequisites

```bash
brew install --cask ubersicht   # desktop widget
brew install --cask swiftbar    # menu-bar dropdown
```

### One-liner

```bash
git clone https://github.com/acamarata/claw-fleet.git
cd claw-fleet
./install.sh
```

For dev mode (symlinks, edits in the repo take effect live):

```bash
./install.sh --dev
```

Other flags: `--widget-only`, `--swiftbar-only`, `--no-launchd`.

After install, open Übersicht and SwiftBar. The widget and menu-bar icon appear within a few seconds.

---

## Configuration

### Credentials by provider

**Claude** — nothing to configure. The probe auto-detects `~/.claude-acc*` config dirs and reads OAuth tokens from the macOS keychain using Claude Code's own service naming.

**Codex** — nothing to configure. The probe auto-detects `~/.codex*` dirs and reads OAuth tokens the same way.

**Gemini** — two modes:

- *Mode A (API key):* set `GEMINI_API_KEY_OPENCLAW` in `~/.claude/vault.env`. Set `GEMINI_GCP_PROJECT_ID` if your project is not `openclaw-io` (that is the default). The probe queries the Gemini API and reports model-level quota utilization.
- *Mode B (Cloud Monitoring):* run `gcloud auth application-default login` once. This upgrades the probe to pull real quota consumption metrics from Cloud Monitoring (`serviceruntime.googleapis.com/quota/rate/net_usage`), which is more accurate than API-key probing. Mode B activates automatically when ADC credentials are present.

**OpenCode Go** — set two vars in `~/.claude/vault.env`:

```bash
OPENCODE_GO_WORKSPACE_ID=<your workspace ID from opencode.ai>
OPENCODE_GO_AUTH_COOKIE=<session cookie value>
```

To get the session cookie: open Chrome, go to `opencode.ai`, open DevTools → Application → Cookies → `opencode.ai`, copy the value of the session cookie. The probe uses this to scrape your workspace usage page. Cookies typically expire in 30 days; re-export when you see `auth_expired` in the cache.

Note: this scrape is a workaround until OpenCode ships a usage API. Track [opencode PR #16513](https://github.com/anomalyco/opencode/pull/16513).

### Optional config file

Auto-detection covers most setups. To customize labels, disable accounts, or supply an explicit list, create `~/.config/claw-fleet/config.json`:

```json
{
  "accounts": [
    { "dir": "~/.claude-acc1", "label": "Main",  "enabled": true },
    { "dir": "~/.claude-acc2", "label": "Work",  "enabled": true },
    { "dir": "~/.claude-acc3", "label": "Old",   "enabled": false }
  ],
  "include_primary": false
}
```

See [examples/config.example.json](examples/config.example.json) for a full example.

---

## How it works

Seven LaunchAgents run the probe and maintenance scripts on staggered schedules:

| Agent | Script | Interval | Role |
|---|---|---|---|
| `io.clawfleet.refresh` | `claw-fleet-refresh` | wrapper | orchestrates the Claude probe + markdown summary |
| `io.clawfleet` | `claw-fleet` | 60s | Claude OAuth probe (A1–A4) |
| `io.clawfleet.codex` | `claw-fleet-codex` | 5min | Codex probe (C1) |
| `io.clawfleet.gemini` | `claw-fleet-gemini` | 5min | Gemini API / Cloud Monitoring probe (G1) |
| `io.clawfleet.opencode` | `claw-fleet-opencode` | 5min | OpenCode Go HTML scrape (O1) |
| `io.clawfleet.watchdog` | `claw-fleet-watchdog` | 60s | encoding-safe pgrep, log truncation, widget self-heal |
| `io.clawfleet.sanity` | `claw-fleet-sanity` | 3600s | deep cache validation, reset-in-past anomaly detection |

Each probe reads its provider and merges results into the shared cache. Unknown top-level keys in the cache are preserved across merges, so probes can run independently without clobbering each other.

The back-off system tracks `back_off_until` per account. On a `rate_limit_error`, the probe backs off exponentially starting at 2 minutes, capping at 10 minutes. The widget suppresses the stale `*` marker while an account is in active back-off.

---

## Gemini Pooling Proxy

Claw Fleet includes a lightweight, zero-dependency local proxy server that pools Gemini API keys across all your Google accounts (G1–G8), rotates them round-robin, and automatically retries requests on rate limits (HTTP 429).

The proxy server runs continuously in the background on `http://localhost:3761`.

### How to use with OpenCode

To route OpenCode's Gemini requests through your local Claw Fleet proxy pool, update your global OpenCode config (`~/.config/opencode/opencode.json`) to point to the proxy's base URL:

```json
  "provider": {
    "google": {
      "options": {
        "baseURL": "http://localhost:3761/v1beta"
      }
    }
  }
```

The proxy dynamically loads all keys starting with `GEMINI_API_KEY_OPENCLAW`, `GEMINI_FREE_KEY_*`, and `GEMINI_KEY_*` from your `~/.claude/vault.env` file.

---

## Uninstall

```bash
./uninstall.sh
```

To also remove the config dir and widget position file:

```bash
./uninstall.sh --purge
```

The cache file (`~/.claude/usage-cache.json`) is kept unless `--purge` is set.

---

## Troubleshooting

See [.github/docs/TROUBLESHOOTING.md](.github/docs/TROUBLESHOOTING.md).

Full architecture: [.github/docs/ARCHITECTURE.md](.github/docs/ARCHITECTURE.md).

---

## License

MIT. Copyright 2026 Aric Camarata. See [LICENSE](LICENSE).
