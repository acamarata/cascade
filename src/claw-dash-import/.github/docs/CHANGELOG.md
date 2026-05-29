# Changelog

## [0.1.0] - 2026-05-13

### Added

- Initial release.
- Übersicht desktop widget: compact project overview with active task count, inbox unread count, and link to full dashboard.
- Web dashboard at `localhost:3077`: full Kanban view, memory browser, inbox reader, and thread task view.
- `~/bin/claw-dash` poller: walks `.claude/` directories, parses tasks, memory, and inbox files, writes `~/.claw-dash/cache.json`.
- `~/bin/claw-dash-server`: persistent HTTP server, serves `src/web/` as static files, exposes `/api/cache`.
- LaunchAgent `io.clawdash.refresh`: runs the poller every 60 seconds.
- LaunchAgent `io.clawdash.server`: keeps the server running as a persistent process.
- Auto-detection of projects under `~/Sites/` and `~/Downloads/` when no config file is present.
- Config file support at `~/.claw-dash/config.json` for explicit project list, labels, and port override.
- Dynamic port selection: finds next available port if configured port is in use, writes active port to `~/.claw-dash/port`.
- Copy buttons on every task row, memory entry, inbox message, thread task, and code block.
- Dark theme throughout.
- Multi-project sidebar navigation in the web dashboard.
- Thread task support: reads `memory/threads/*/tasks/*.md` and displays per-thread.
- Read-only: never writes to any `.claude/` directory.
