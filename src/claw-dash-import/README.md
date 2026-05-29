# Claw Dash

A macOS visual dashboard for Claude Code's `.claude/` project management system.

Reads `.claude/` directories and renders them as a live dashboard across two surfaces:

- **Desktop widget** (Übersicht, overlay, auto-refresh every 5s)
- **Web dashboard** (browser, `localhost:3077`, full Kanban + memory + inbox view)

<!-- TODO: add badges once GitHub repo exists (npm version, CI status, license) -->
<!-- TODO: add screenshot once installed -->

---

## What it shows

The widget gives you a compact overview: active task count, inbox unread count, and a link to the full dashboard. One glance tells you what's in flight across all your projects.

The web dashboard shows everything:

- **Tasks board**: active, queued, and done tasks parsed from `tasks/*.md`, with status emoji, weight tags, and blockers called out
- **Memory files**: all `memory/*.md` entries with timestamps and full text
- **Inbox**: unread messages from `inbox/*.md`, sorted by priority
- **Thread tasks**: tasks inside `memory/threads/*/tasks/` shown per-thread
- **Multi-project navigation**: switch between projects from a sidebar

---

## Features

- Auto-detects `.claude/` directories under common project paths. No hardcoded list required.
- Read-only. Never writes to your `.claude/` directories.
- Live reload: the web dashboard polls the cache every 5 seconds. The widget refreshes on the same interval.
- Copy buttons on every task, memory entry, inbox message, and code block. One click to clipboard.
- Dark theme, optimized for desktop.
- Multi-project: all configured projects visible in one view, switchable without reload.

---

## Install

### Prerequisites

```bash
# Übersicht (desktop widget)
brew install --cask ubersicht

# python3 (ships with macOS, but verify)
python3 --version
```

### One-liner

```bash
git clone https://github.com/acamarata/claw-dash.git
cd claw-dash
./install.sh
```

For dev mode (symlinks, edits in repo take effect live):

```bash
./install.sh --dev
```

After install, open Übersicht. The widget appears within a few seconds. The web dashboard is available at `http://localhost:3077`.

---

## How it works

A LaunchAgent runs `~/bin/claw-dash` every 60 seconds. The poller:

1. Reads `~/.claw-dash/config.json`, or auto-detects projects under `~/Sites/` and `~/Downloads`.
2. For each project, walks the `.claude/` directory tree.
3. Parses `tasks/active.md`, `tasks/queue.md`, `tasks/done.md`, `memory/*.md`, `inbox/*.md`, and thread task files.
4. Writes structured output to `~/.claw-dash/cache.json`.

A second LaunchAgent runs `~/bin/claw-dash-server` as a persistent process. It serves the web dashboard from `src/web/` and exposes `/api/cache` backed by the cache file. It writes the active port to `~/.claw-dash/port`.

Both the Übersicht widget and the browser read from the cache. No direct filesystem access happens at display time.

Full data-flow diagram: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

---

## The `.claude/` directory standard

Claw Dash reads the following layout. This is the standard Claude Code project management structure.

```
.claude/
├── tasks/
│   ├── active.md      (current phase tasks)
│   ├── queue.md       (upcoming phases)
│   └── done.md        (completed tasks)
├── memory/
│   ├── MEMORY.md      (index file)
│   ├── *.md           (individual memory files)
│   └── threads/       (thread-based task system)
│       └── {name}/
│           ├── README.md
│           └── tasks/
│               ├── planned.md
│               ├── open.md
│               ├── review.md
│               └── done.md
└── inbox/
    └── *.md           (unread messages)
```

Claw Dash reads all of this and nothing else. Files outside `.claude/` are not touched.

---

## Configuration

Without a config file, Claw Dash auto-detects projects by scanning `~/Sites/*/` and `~/Downloads/` for `.claude/` subdirectories.

To customize, create `~/.claw-dash/config.json`:

```json
{
  "port": 3077,
  "projects": [
    { "path": "~/Downloads", "label": "Downloads", "enabled": true },
    { "path": "~/Sites/nself", "label": "nSelf", "enabled": true }
  ]
}
```

See [examples/config.example.json](examples/config.example.json) for a full example.

When a config file exists, auto-detection is disabled. Only listed projects are shown.

---

## Port configuration

The server defaults to port `3077`. To use a different port, set it in `~/.claw-dash/config.json`:

```json
{ "port": 4000 }
```

If the configured port is in use, the server finds the next available port and writes it to `~/.claw-dash/port`. The widget reads this file to construct the dashboard URL, so the "Open Dashboard" link always points to the correct port regardless of what port the server landed on.

---

## Copy buttons

Every task row, memory entry, inbox message, thread task, and code block has a copy button. The button copies the raw markdown text. For tasks, it copies the full task line including status emoji and metadata.

---

## Uninstall

```bash
./uninstall.sh
```

To also remove the config dir and cache:

```bash
./uninstall.sh --purge
```

---

## Troubleshooting

Common issues: cache empty, server not running, port conflict, widget not appearing, projects not detected.

See [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md).

---

## License

MIT. Copyright 2026 Aric Camarata. See [LICENSE](LICENSE).
