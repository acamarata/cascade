# Scheduler

The Cascade daemon includes a built-in scheduled-task execution engine. It runs as a Tokio background task and executes shell-free child processes on cron, interval, or one-time schedules.

## Overview

The scheduler polls the task store every 30 seconds (configurable) for tasks whose `next_run` is due. For each due task, it spawns a child process using an arg-array (no shell string interpolation), captures stdout and stderr, and updates the task status in the store.

## Configuration

Add a `[scheduler]` section to `~/.cascade/config.toml`:

```toml
[scheduler]
enabled = true
poll_interval_secs = 30
max_parallel = 4
```

| Field | Default | Description |
|---|---|---|
| `enabled` | `true` | Enable the scheduled-task engine |
| `poll_interval_secs` | `30` | Seconds between polls for due tasks |
| `max_parallel` | `4` | Maximum tasks running simultaneously |

## Schedule types

Three schedule types are supported:

**Cron**: runs on a cron expression (5- or 6-field):

```json
{ "type": "cron", "expression": "0 9 * * *" }
```

**Interval**: runs every N seconds after the last run:

```json
{ "type": "interval", "secs": 3600 }
```

**Once**: runs exactly once at a specific UTC datetime:

```json
{ "type": "once", "at": "2026-06-15T09:00:00Z" }
```

## Task lifecycle

1. Task is inserted into the store with `enabled = true` and `next_run` set (or null for new tasks).
2. On the next poll, the scheduler finds the task via `get_due(now)`.
3. For **Once** tasks: `enabled` is set to `false` atomically in the store before the process is spawned, preventing double-run on daemon restart.
4. The child process runs: `Command::new(command).args(args)`.
5. On success: `status = Success { ran_at, duration_ms }`, `next_run` computed from the schedule.
6. On failure (non-zero exit): `status = Failed { ran_at, error }` with stderr truncated to 512 chars.

## Security

- Commands are never run through a shell. The `command` field is passed directly to `execvp()` and `args` are individual arguments: no shell expansion, no injection surface.
- stderr is capped at 512 characters to prevent unbounded memory allocation from runaway tasks.
- Tasks run with the daemon user's permissions. There is no sandboxing in P2; sandboxing is planned for a future phase.

## Status fields

| Status | Meaning |
|---|---|
| `Pending` | Has not run yet |
| `Running` | Currently executing (set transiently) |
| `Success { ran_at, duration_ms }` | Completed with exit code 0 |
| `Failed { ran_at, error }` | Non-zero exit or OS-level spawn error |

## Parallel execution

The scheduler uses a `tokio::sync::Semaphore` with `max_parallel` permits. When the permit count is exhausted, additional due tasks wait until a running task finishes and releases its permit. Tasks are dispatched in `next_run ASC NULLS FIRST` order.
