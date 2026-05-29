# Architecture

## Data Flow

```
 ┌──────────────────────────────────────────────────┐
 │        LaunchAgent (every 60s)                    │
 │        io.clawdash.refresh                        │
 │        ~/bin/claw-dash                            │
 └──────────────────────┬───────────────────────────┘
                        │ runs
                        ▼
 ┌──────────────────────────────────────────────────┐
 │        claw-dash (poller)                         │
 │  1. Read ~/.claw-dash/config.json                 │
 │     (or auto-detect ~/Sites/*/ + ~/Downloads/)    │
 │  2. For each project: walk .claude/               │
 │     - tasks/active.md, queue.md, done.md          │
 │     - memory/*.md (including MEMORY.md)           │
 │     - memory/threads/*/tasks/*.md                 │
 │     - inbox/*.md                                  │
 │  3. Parse markdown into structured objects        │
 │  4. Write → ~/.claw-dash/cache.json               │
 └──────────────────────┬───────────────────────────┘
                        │ writes
                        ▼
              ~/.claw-dash/cache.json
                        │
           ┌────────────┴────────────┐
           ▼                         ▼
 ┌─────────────────┐       ┌──────────────────────┐
 │  LaunchAgent    │       │  claw-dash.widget     │
 │  (persistent)   │       │  (Übersicht, 5s)      │
 │  io.clawdash    │       │                       │
 │  .server        │       │  reads cache + port   │
 │                 │       │  → compact overlay    │
 │  ~/bin/claw-    │       │  "Open Dashboard →"   │
 │  dash-server    │       │  opens localhost:PORT │
 │                 │       └──────────────────────┘
 │  finds free port│
 │  writes port →  │
 │  ~/.claw-dash/  │
 │  port           │
 │                 │
 │  serves         │
 │  src/web/ at    │
 │  localhost:PORT │
 │                 │
 │  GET /api/cache │
 │  → cache.json   │
 └─────────────────┘
           │
           ▼
 ┌──────────────────────┐
 │  Browser             │
 │  localhost:PORT      │
 │                      │
 │  polls /api/cache    │
 │  every 5s            │
 │  full Kanban /       │
 │  memory / inbox view │
 └──────────────────────┘
```

All consumers are read-only. Only `claw-dash` (the poller) writes the cache.

---

## Cache Schema

```json
{
  "generated_at": 1747000000,
  "projects": [
    {
      "path": "~/Sites/nself",
      "label": "nSelf",
      "scanned_at": 1747000000,
      "error": null,
      "tasks": {
        "active": [
          {
            "raw": "🚧 TASK-001 [M] Fix auth edge case",
            "status": "active",
            "weight": "M",
            "text": "Fix auth edge case",
            "id": "TASK-001"
          }
        ],
        "queue": [],
        "done": []
      },
      "memory": [
        {
          "file": "decisions.md",
          "modified_at": 1746900000,
          "content": "..."
        }
      ],
      "threads": [
        {
          "name": "auth-refactor",
          "readme": "...",
          "tasks": {
            "planned": [],
            "open": [],
            "review": [],
            "done": []
          }
        }
      ],
      "inbox": [
        {
          "file": "msg-2026-05-13-bug-report.md",
          "modified_at": 1747000000,
          "content": "..."
        }
      ]
    }
  ]
}
```

Key fields:

| Field | Type | Notes |
|---|---|---|
| `generated_at` | Unix timestamp | When the poller last wrote this file |
| `projects[].path` | string | Expanded `~` path |
| `projects[].label` | string | From config, or directory basename |
| `projects[].scanned_at` | Unix timestamp | When this project was last scanned |
| `projects[].error` | string or null | Set when `.claude/` is missing or unreadable |
| `projects[].tasks.active` | array | Tasks from `tasks/active.md` |
| `projects[].tasks.queue` | array | Tasks from `tasks/queue.md` |
| `projects[].tasks.done` | array | Tasks from `tasks/done.md` |
| `projects[].memory` | array | All `memory/*.md` files |
| `projects[].threads` | array | Thread directories from `memory/threads/` |
| `projects[].inbox` | array | All `inbox/*.md` files |

---

## Config Schema

`~/.claw-dash/config.json`:

| Field | Type | Default | Notes |
|---|---|---|---|
| `port` | number | `3077` | Preferred server port |
| `projects` | array | (auto-detect) | Explicit project list |
| `projects[].path` | string | required | Absolute or `~`-prefixed path |
| `projects[].label` | string | directory basename | Display name |
| `projects[].enabled` | boolean | `true` | Set `false` to hide without removing |

When `projects` is absent or the file does not exist, the poller scans `~/Sites/*/` and `~/Downloads/` for directories containing `.claude/`.

---

## Port File

`~/.claw-dash/port` contains a single line: the port number the server is currently bound to.

The Übersicht widget reads this file to build the `http://localhost:PORT` URL for the "Open Dashboard" button. This means the button still works if the server landed on a different port due to a conflict.

The server rewrites this file each time it starts.

---

## Component Map

| Component | Location | Purpose |
|---|---|---|
| `src/bin/claw-dash` | `~/bin/claw-dash` (installed) | Project discovery, `.claude/` walk, cache write |
| `src/bin/claw-dash-server` | `~/bin/claw-dash-server` (installed) | HTTP server, static file serving, `/api/cache` |
| `src/widget/claw-dash.widget/index.jsx` | `~/Library/Application Support/Übersicht/widgets/` | Desktop widget, reads cache and port file |
| `src/web/` | served at `localhost:PORT` | Full web dashboard (HTML/CSS/JS) |
| `src/launchd/io.clawdash.refresh.plist.template` | `~/Library/LaunchAgents/io.clawdash.refresh.plist` (rendered) | Schedules poller every 60 seconds |
| `src/launchd/io.clawdash.server.plist.template` | `~/Library/LaunchAgents/io.clawdash.server.plist` (rendered) | Keeps server running persistently |
| `~/.claw-dash/config.json` | (optional, user-created) | Project list and port override |
| `~/.claw-dash/cache.json` | (auto-written by poller) | Parsed project data, source of truth for all consumers |
| `~/.claw-dash/port` | (auto-written by server) | Active server port number |
