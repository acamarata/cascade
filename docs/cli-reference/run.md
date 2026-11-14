# cascade run

`cascade run --task <class> [--input role:content]...` dispatches one model
task through the daemon's conductor. The flags below cover fan-out; see
`cascade run --help` for the rest.

## Fan-out (`--fan-out N`, hidden)

`--fan-out N` with 2 <= N <= 16 asks the conductor for N parallel legs. A
value above 16 is refused before the daemon is contacted.

Before it dispatches, the command mints a request id and prints it to
stderr:

```
cascade: request id 01J9Z3NDEKTSV4RRFFQ69G5FAV
```

The daemon echoes the id in its response. If it does not, the command
refuses with "daemon does not support re-attach". A daemon older than this
feature ignores the field and still runs one paid single dispatch before the
command sees the missing echo. That is accepted because the CLI and the
daemon ship as one binary.

## Re-attach (`--resume <request id>`, hidden)

If a fan-out call was cut off (the client died or the daemon restarted),
run the same command again with `--resume <request id>`. Legs that already
finished are returned without a new provider call; only the missing legs
run. A changed prompt is refused as a conflict. `--resume` requires
`--fan-out` of 2 or more.

## Delegate to an agent (`cascade run --agent <intent>`)

```
cascade run --agent <intent> [--task-class c] [--min-quality q]
    [--deadline d] [--mutation] [--job id] [--timeout dur]
```

`cascade run --agent` hands an intent to the daemon and waits for the result.
The intent is the value of `--agent`, for example `--agent "list files"`.
`--task` is not needed with `--agent`. The command calls
`conductor.delegate`, then `conductor.delegate_wait` with the job id it was
given. The daemon alone starts agent processes; the command never starts one.

| Flag | Meaning |
|------|---------|
| `--agent <intent>` | The intent to delegate. Required, not empty. |
| `--task-class <c>` | Task class of the delegated work. Default `reason`, or `code` with `--mutation`. |
| `--min-quality <q>` | Minimum quality: `standard`, `high` or `max`. |
| `--deadline <d>` | Deadline class: `interactive` or `batch`. |
| `--mutation` | The delegation may change files. |
| `--job <id>` | Attach the delegation to this job id. |
| `--timeout <dur>` | How long to wait for the result. Default `5m`; must not be negative. |

Output is the agent output on stdout and a `job_id=... state=...` line on
stderr. With `--json` the command prints one envelope whose data holds
`job_id`, `state`, `output`, `artifacts` and `data_class`.

### No daemon

If the daemon is not running, the command refuses with an `unavailable`
error ("the daemon is not running"). This holds for an embedded (daemonless)
run and for a socket that refuses the dial. Nothing is run inline as a
fallback.

### Non-interactive use

`cascade run --agent` never prompts. With `CASCADE_NO_INPUT=1` and the flags
given, it reads no stdin and completes or fails exactly as it would
interactively.

### Windows

On Windows there is no daemon, so the command refuses with an `unsupported`
error.
