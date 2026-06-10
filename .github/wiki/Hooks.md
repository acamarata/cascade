# Hooks

Cascade hooks enable you to execute custom actions when daemon events occur. Hooks connect daemon lifecycle events (startup, shutdown, cascade changes, task completion) to user-defined actions (run a script, log a message, publish an event).

---

## Overview

A hook is a named trigger that fires when a specific event occurs in the daemon. Each hook specifies:

- **Event** — what daemon event to listen for (DaemonStart, CascadeChanged, TaskDone, etc.)
- **Action** — what to do when the event fires (RunCommand, LogMessage, PublishEvent)
- **Enabled** — whether the hook is active (true/false)

Hooks are defined in each tier's `config.toml` as `[[hooks]]` arrays. The daemon loads hooks on startup, queries them when events occur, and executes matching hooks in tier priority order (GCI hooks fire first; PAC hooks fire last).

All hook failures are isolated — a failing hook does not crash the daemon or prevent other hooks from running.

---

## Event Reference

The `HookEvent` enum defines all event types the daemon can fire. Your hook must match one of these.

| Event | Fired when | Carries |
|---|---|---|
| `DaemonStart` | Daemon startup sequence completes successfully | — |
| `DaemonStop` | Daemon graceful stop is initiated (clean shutdown) | — |
| `CascadeChanged` | Any `.cascade/` file is modified or added (any tier) | — |
| `RegenComplete` | Cascade compat files are regenerated (AGENTS.md, CLAUDE.md) | — |
| `BackupComplete` | A backup sync completes (any tier's snapshots rotated) | — |
| `TaskDone` | A scheduled task completes (cron, interval, once) | `task_id` (UUID as string) |
| `Custom` | User-defined event published by a hook or external tool | `kind` (arbitrary string) |

### Wildcard matching for TaskDone

When listening for `TaskDone`, use `task_id = "*"` to match any completed task:

```toml
[[hooks]]
name = "log-any-task-done"
event = { type = "TaskDone", task_id = "*" }
action.type = "LogMessage"
action.level = "info"
action.message = "A task completed"
```

To match a specific task, use its UUID:

```toml
[[hooks]]
name = "log-my-backup-task"
event = { type = "TaskDone", task_id = "550e8400-e29b-41d4-a716-446655440000" }
action.type = "LogMessage"
```

---

## Action Types

Three action types let you respond to hook events.

### RunCommand

Execute an external command in a child process.

**Fields:**
- `command` (string, required) — the binary name or absolute path
- `args` (list of strings, optional) — command arguments

**Safety:**
- Arguments are passed as an array, never interpolated through a shell. This prevents command injection.
- Command execution timeout is 30 seconds. Commands that run longer are killed.
- Failures are logged but do not crash the daemon.

**Example:**

```toml
[[hooks]]
name = "notify-on-regen"
event = "RegenComplete"
[hooks.action]
type = "RunCommand"
command = "sh"
args = ["-c", "echo 'Cascade regenerated' | mail -s 'Cascade Alert' admin@example.com"]
```

### LogMessage

Emit a structured log message at the specified level.

**Fields:**
- `level` (string, default "info") — trace, debug, info, warn, error
- `message` (string, required) — the log text

**Example:**

```toml
[[hooks]]
name = "log-daemon-start"
event = "DaemonStart"
[hooks.action]
type = "LogMessage"
level = "info"
message = "Cascade daemon started successfully"
```

### PublishEvent

Publish a custom event onto the daemon's internal event bus, making it available to other hooks.

**Fields:**
- `kind` (string, required) — the event kind string (e.g. "custom.backup.completed")
- `payload` (JSON object, optional) — arbitrary structured data

**Restrictions:**
- `DaemonStart` and `DaemonStop` events cannot be published from a hook (prevents feedback loops). Attempting to publish them logs a warning and silently skips the publish.

**Example:**

```toml
[[hooks]]
name = "publish-regen-signal"
event = "RegenComplete"
[hooks.action]
type = "PublishEvent"
kind = "custom.regen.occurred"
payload = { timestamp = "2026-06-02T10:30:00Z", tier = "PAC" }
```

---

## Authoring Hooks in config.toml

Hooks are defined as `[[hooks]]` arrays in any tier's `config.toml`. The `[[hooks]]` directive allows multiple hook definitions in one file.

### Basic syntax

```toml
[[hooks]]
name = "my-hook"
event = "<event>"            # or { type = "...", ... } for events with data
[hooks.action]
type = "<action>"
# ... action-specific fields ...
enabled = true               # optional, defaults to true
```

### Real-world examples

#### Example 1: Log cascade changes at info level

```toml
[[hooks]]
name = "log-cascade-changes"
event = "CascadeChanged"
[hooks.action]
type = "LogMessage"
level = "info"
message = "A cascade file changed; AGENTS.md and CLAUDE.md will be regenerated"
```

#### Example 2: Run a custom script when a task completes

```toml
[[hooks]]
name = "post-task-cleanup"
event = { type = "TaskDone", task_id = "*" }
[hooks.action]
type = "RunCommand"
command = "/opt/cascade/hooks/post-task.sh"
args = []
```

#### Example 3: Publish a monitoring event for external tools

```toml
[[hooks]]
name = "publish-backup-event"
event = "BackupComplete"
[hooks.action]
type = "PublishEvent"
kind = "monitoring.backup.complete"
payload = { severity = "info", source = "cascade" }
```

---

## Hook Ordering and Execution

Hooks execute in **tier priority order** when an event fires:

1. **GCI hooks** (global) fire first — `~/.cascade/config.toml`
2. **PPI/PRI/PAC hooks** (project) fire next — project-specific `config.toml` files
3. When multiple hooks match the same event, execution order is the order they appear in the config file

All hooks for a matching event are executed (unless filtering by task_id or custom kind), and failures in one hook do not prevent the next from running.

---

## Timeouts and Failure Handling

### Timeouts

Each hook execution has a **30-second timeout**. If a hook (particularly `RunCommand`) exceeds 30 seconds, it is killed and logged as a timeout error. The daemon continues normally.

### Failure isolation

If a hook fails for any reason:
- The error is logged (with `error!()` at ERROR level)
- The failure does not propagate to the daemon
- Other hooks continue to execute
- The daemon does not crash or become unstable

Example error log (a RunCommand that exits with status 1):

```
ERROR cascade_daemon::hook_runner: hook 'my-failing-hook' failed: command exited with status 1
```

---

## Limitations in P2

Hooks are fully functional in P2, but some features are deferred to P3 and beyond:

| Feature | Status | Plan |
|---------|--------|------|
| Hook definition in config.toml | ✅ P2 | Already shipping |
| Hook event subscription | ✅ P2 | Already shipping |
| Hook action execution (RunCommand, LogMessage, PublishEvent) | ✅ P2 | Already shipping |
| Hook dashboard UI (view/edit/test hooks) | P3 | Web dashboard in phase 3 |
| Hook CRUD via IPC | P3 | Full lifecycle management API in E-03 |
| Hook output capture and logging | P3 | Capture stdout/stderr to logs |
| Hook scheduling (run hook at a specific time) | P4 | Defer until scheduler integration |

---

## Next Steps

- Learn how to schedule tasks: [Scheduler](Scheduler.md)
- Understand daemon lifecycle: [Daemon Architecture](Daemon-Architecture.md)
