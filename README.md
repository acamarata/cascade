# cascade

Cascading fleet dashboard and Gemini proxy relay for multi-agent Claude / OpenCode sessions.

Absorbed from `claw-fleet` (Gemini proxy daemon) and `claw-dash` (fleet dashboard) — merged into a single unified tool in May 2026.

## What it does

- **Gemini proxy** (`src/bin/cascade-gemini-proxy`): runs on `localhost:3761`, rotates across 28 Gemini API keys from vault, writes utilization to `~/.claude/temp/quota-state.json`
- **Fleet dashboard** (`src/web/`): reads `quota-state.json`, renders per-account utilization as a web UI on `localhost:9761`
- **launchd agents**: both services run as user launchd agents, auto-start on login, restart on crash

## Ports

| Service | Port | Protocol | Purpose |
|---|---|---|---|
| `cascade-gemini-proxy` | 3761 | HTTP | Gemini API relay + quota tracking |
| `cascade-dashboard` | 9761 | HTTP | Fleet utilization dashboard |

## Install

```bash
bash install.sh
```

## Uninstall

```bash
bash uninstall.sh
```

## launchd agents

cascade runs three persistent user launchd agents (no admin required, auto-start on login, restart on crash).

| Label | Service | Port | Log |
|---|---|---|---|
| `io.cascade.gemini-proxy` | Gemini API relay | 3761 | `~/Library/Logs/cascade-gemini-proxy.log` |
| `io.cascade.dashboard` | Fleet dashboard | 9761 | `~/Library/Logs/cascade-dashboard.log` |
| `io.cascade.refresh` | Quota poller (every 15s) | — | `~/Library/Logs/cascade-dash-refresh.log` |

### Status

```bash
launchctl list | grep cascade
```

### Manual start / stop

```bash
# start individual agent
launchctl load ~/Library/LaunchAgents/io.cascade.gemini-proxy.plist
launchctl load ~/Library/LaunchAgents/io.cascade.dashboard.plist
launchctl load ~/Library/LaunchAgents/io.cascade.refresh.plist

# stop individual agent
launchctl unload ~/Library/LaunchAgents/io.cascade.gemini-proxy.plist
launchctl unload ~/Library/LaunchAgents/io.cascade.dashboard.plist
launchctl unload ~/Library/LaunchAgents/io.cascade.refresh.plist
```

### Log files

```bash
tail -f ~/Library/Logs/cascade-gemini-proxy.log
tail -f ~/Library/Logs/cascade-dashboard.log
tail -f ~/Library/Logs/cascade-dash-refresh.log
```

## Architecture

```
vault.env (28 Gemini keys, G1-G8)
    └─ cascade-gemini-proxy (localhost:3761)
           └─ quota-state.json (~/.claude/temp/)
                  └─ cascade-dashboard (localhost:9761)
```

## Data flow

The cascade pipeline has four stages:

1. **cascade-gemini-proxy** (localhost:3761) reads 28 Gemini API keys from `~/.claude/vault.env` and rotates them round-robin across requests, auto-retrying on HTTP 429. Writes quota utilization data to `~/.claude/temp/quota-state.json`.

2. **cascade-refresh** (called by launchd every 5 minutes) polls Anthropic and OpenCode APIs for quota usage, aggregates per-account data, and writes the complete quota state to `~/.claude/temp/quota-state.json`. Also generates a human-readable summary to `/tmp/cascade-summary.txt`.

3. **cascade-dash-server** (localhost:9761) runs as a user launchd agent. On startup, it locates the static web directory, finds a free port (starting at 9761), and begins serving HTTP. Requests to `/api/cache` read and serve the quota state from `~/.claude/temp/quota-state.json`. Requests to other paths serve the static web files from `src/web/`.

4. **Dashboard UI** (cascade/src/web/) polls cascade-dash-server every N seconds, fetches the quota state via `/api/cache`, and renders utilization tables and charts showing per-account Gemini API status.

## Migration history

Absorbed from two prior repos in E12 (2026-05-29):
- `claw-fleet` — Gemini proxy daemon + launchd agents (local snapshot, GitHub deleted 2026-05-27)
- `claw-dash` — fleet dashboard web UI + widget (GitHub: acamarata/claw-dash, archived 2026-05-29)
