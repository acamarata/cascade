# Claw Fleet Docs

This local project documentation covers Claw Fleet, a macOS usage dashboard for multiple local coding accounts.

## Pages

- [Architecture](ARCHITECTURE.md)
- [Account Auto-Detection](AUTO_DETECTION.md)
- [Provider Usage Limits](limits/README.md) — per-provider reference for Claude, Codex, Gemini, OpenCode Go
- [Contributing](CONTRIBUTING.md)
- [Troubleshooting](TROUBLESHOOTING.md)
- [Token Refresh History](HISTORY-token-refresh.md)

## Project Scope

- Monitor usage across four providers: Claude (A1–A4), Codex (C1), Gemini API (G1), and OpenCode Go (O1).
- Run tiered probes: Claude every 60s, others every 5min, sanity check hourly.
- Preserve last-known values when a refresh fails; flag stale accounts with `*`.
- Feed terminal (`/aa`), SwiftBar, and Übersicht consumers from one local cache (`~/.claude/usage-cache.json`).

