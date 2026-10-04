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
