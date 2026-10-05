# `cascade doctor`

Runs the registered diagnostic checks against this installation and
reports each result. It exits non-zero when any check reports a warning or
an error, so it can gate a script or a CI step.

```
cascade doctor [--first-run] [--fix] [--harness] [--storage] [--json]
```

| Flag | Effect |
|---|---|
| `--first-run` | run only the checks tagged for a first-run health check |
| `--fix` | try to remediate every fixable check that reports a problem (asks to confirm) |
| `--harness` | run only the coding-harness detection and instruction-drift check |
| `--storage` | run only the storage health probes |

`--harness` and `--storage` are filters over the one registry, not second
code paths. If both are given, `--storage` wins. A filter that selects no
registered check is refused, never reported as a clean empty run.

## Checks that read the live daemon

The census and `hook-events` need the daemon. They ask it once per run
through `status.get` over its socket. **When no daemon answers, each
reports a warning, `daemon not running`, and its check does not run.** An
unreachable daemon is never reported as a pass, so a machine with no daemon
running exits non-zero from a plain `cascade doctor`. Start the daemon with
`cascade daemon start` to clear those rows. `completion-gate-hooks` and
`storage` use the same answer, but they have a database to fall back on.

### `subsystem_census`

Compares the subsystems the daemon declared with what is running. A
declared subsystem that is not running is an error and is named in the
detail. A subsystem whose state is `disabled` (an operator's choice) or
`skipped` (a disclosed precondition that is absent, such as an
unconfigured bridge) is not counted and is not a failure; both are listed
in the detail as `not counted`. A clean machine with an unconfigured
bridge exits 0.

### `hook-events`

Reads `hooks.unknown_event_drops` from `status.get`. The hook handler drops
events whose type it does not recognise without failing the harness, so a
harness newer than the daemon is otherwise invisible. A non-zero count is a
warning that names the count; the count is since the daemon started.

### `completion-gate-hooks`

Reports an error when the primary harness is installed but its settings
file does not name the completion-gate hook, and OK when no harness is
installed. It also warns when the daemon is down while a job is active.
When `status.get` answers, the daemon is reachable and the check asks
nothing more. When it does not, the check opens `<data dir>/cascade.db`
read-only (`mode=ro`, no schema applied) and looks for a job in the
`leased`, `running`, `verifying`, `reviewing` or `cancelling` state:

| Database | Result |
|---|---|
| a job in one of those states | warning: daemon unreachable while a job is active |
| cannot be opened or read | warning (an unknown is not a pass) |
| no `cascade.db` at all | OK (nothing has ever run) |
| only jobs in other states | OK |

### `storage`

Probes `cascade.db`: WAL mode, schema version, domain tables, a write
round trip, and the exclusive lock. **Only `cascade doctor --storage` runs
it**; the default report leaves it out because the write probe inserts and
deletes a sentinel row. Any failing probe is an error, and the detail names
each one. The check opens the existing file and never creates it: with no
`cascade.db` it warns and tells you to start the daemon once. A held
exclusive lock is the running daemon's own, so it is not a failure while
`status.get` answers; with no daemon it is reported as a conflict.

## `config-permissions`

Reports what `runtime.CheckConfigPermissions` says about `config.toml`, the
same classifier `Load` applies, so doctor and a daemon start cannot
disagree:

| Finding | Result |
|---|---|
| refuse (world-writable file, wrong owner, unsafe parent directory) | error |
| warn (group-writable, world-readable) | warning |
| ok (or no config file) | OK |
| not checked (Windows has no unix mode bits) | OK, detail says `not checked` |

The check is read-only and never changes the file's mode; the remediation
text names the `chmod` to run.
