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

## Architecture

```
vault.env (28 Gemini keys, G1-G8)
    └─ cascade-gemini-proxy (localhost:3761)
           └─ quota-state.json (~/.claude/temp/)
                  └─ cascade-dashboard (localhost:9761)
```

## Migration history

Absorbed from two prior repos in E12 (2026-05-29):
- `claw-fleet` — Gemini proxy daemon + launchd agents (local snapshot, GitHub deleted 2026-05-27)
- `claw-dash` — fleet dashboard web UI + widget (GitHub: acamarata/claw-dash, archived 2026-05-29)
