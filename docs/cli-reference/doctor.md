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

## Security checks

Six checks report the security posture. All are read-only: they open the
audit store only with SQLite `mode=ro` (or `immutable=1` when no `-wal` or
`-shm` exists), never take the store lock, never select writing custody, and
never write a row, a key or a config. With a daemon holding the store, SQLite
may update `cascade.db-shm` (its derived read-mark index) while `cascade.db`
and `cascade.db-wal` stay byte-identical; that is the one file touch. A
machine that has never run cascade has no `cascade.db`; the checks that read
it then report OK and create nothing.

| Check | Status | Meaning |
|---|---|---|
| `elevation_custody` | OK | `tier=platform` with an enrolled key bound to it: hardware-backed custody |
| | warning | `tier=platform` but no enrolled key is bound to it: elevated verbs refuse; run `cascade elevate-helper --enroll` |
| | warning | `tier=presence`: weaker than hardware custody, with the residual below |
| | warning | `tier=file`: a retired tier, elevated verbs refuse; run `cascade elevate-helper --enroll --replace-file-tier` |
| | warning | `tier=none`: no protected key source; elevated verbs refuse, and the reason is shown |
| `audit_chain` | OK | no `cascade.db` yet, or the whole chain verifies |
| | warning | `audit chain not verified`: the store is changing under the check (a lone `-wal` or `-shm`, or a database with no tables yet); run doctor again |
| | error | the store cannot be opened read-only, a database has other tables but no `kv` table, or verification fails twice; the message names the record |
| `policy` | OK | the `[policy]` profile is named and the deny rules are readable |
| | warning | `config.toml` or `[policy]` does not parse, or the deny store is unreadable |
| `approval_key` | OK | the key is stored and consistent, or not stored yet (generated at first daemon run) |
| | warning | the vault or the stored key is unreadable or inconsistent |
| `egress_audit_only` | OK | lists each class that has no enforced transit path, with the reason |
| `caller_principal` | OK | states the threat model: principals protect model-facing surfaces, not a boundary against shell code running as the same user. It reports no credential counts: they live only in the daemon's memory |

On the presence tier, `elevation_custody` also reads the audited
`elevation.presence.enroll` records and warns when the enrolled fingerprint
differs from the last audited enrolment, when no enrolment was audited, or
when a fresh (non-rotation) enrolment was audited after an earlier one, which
is what deleting the key and enrolling again looks like. A failed enrolment is
ignored. The residual is printed in the message, as in
[the presence tier page](../elevation-presence.md): a process running as your
user can replace `presence.key` and `trust.json` together, and doctor and the
audit log are where that shows.

### How the read-only open behaves

`audit_chain`, `policy` and the presence history open `<data dir>/cascade.db`
without the store lock, the migrator or any write, and they create no file.
SQLite's plain read-only mode would create `cascade.db-wal` and
`cascade.db-shm` on a database its writer closed cleanly, and could not
remove them, so the open depends on which of the two exist:

| `-wal` and `-shm` | Open | Why |
|---|---|---|
| neither | `immutable=1` | A cleanly stopped daemon leaves the main file as the whole state; nothing is created or locked. |
| both | `mode=ro` | A running daemon holds the store. The check reads the committed snapshot while the daemon writes, and changes neither `cascade.db` nor `-wal`. |
| exactly one (an empty `-wal` alone counts as none) | refused | SQLite creates and removes the pair one file at a time, so a daemon that is starting or stopping shows this for a moment, and a crashed one leaves it. Opening would need a write. The check reports a warning (`audit chain not verified`), never an error and never OK. Run doctor again; if it persists, start the daemon once so it recovers the log. |

The `immutable=1` open takes no lock, so a writer that starts and checkpoints between the open and the verification can make the first verification fail; when the file changed or a sidecar appeared meanwhile, the check reopens and verifies once more before reporting tampering. A `cascade.db` with no tables at all is also a transition (another command or
a starting daemon is creating the store) and gets the same warning; one with other tables but no `kv` table is an error.

With no `cascade.db` the checks report OK and create nothing. A `cascade.db`
symlink whose target is gone is an error, not "no audit log yet".
