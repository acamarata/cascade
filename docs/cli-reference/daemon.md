# `cascade daemon`

Skeleton reference for the `daemon` noun (07-CLI-COMMAND-TREE.md §daemon).
D/S-06.T2 (daemon lifecycle sprint, same wave) owns the cobra mount for
`run` / `start` / `stop` / `restart` / `status` / `install` / `uninstall`.
This page documents the one subcommand this ticket
(P1-E03-W1-S04-T2) implements ahead of that: `logs`.

## `cascade daemon logs [-f]`

Reads the single log file every daemon subsystem writes to
(`internal/runtime.LogFilePath`, under `PathProvider.LogDir()`) and
prints it to stdout. **Does not require a live daemon process** — the
handler reads the file directly; if the daemon has never run, it prints
a diagnostic to stderr and exits cleanly rather than erroring.

```
$ cascade daemon logs
{"time":"2026-09-02T10:00:00Z","level":"INFO","msg":"daemon started"}
{"time":"2026-09-02T10:00:01Z","level":"INFO","msg":"provider registered","name":"claude"}
```

`-f` (follow) keeps streaming new lines as they are appended, polling via
`os.Stat` (no inotify/FSEvents dependency — R-14.115: no new module
dependency for this ticket), until interrupted:

```
$ cascade daemon logs -f
...
```

If the log file disappears while following (log directory removed,
rotated out from under the reader, ...), the handler prints a diagnostic
to stderr and exits cleanly — this is not treated as a failure.

Output contract (D/S-06.T5): log content goes to **stdout**; diagnostics
(missing file, disappeared file) go to **stderr**.

### Implementation status

`internal/runtime.DaemonLogsHandler` is the real, fully implemented
capability behind this subcommand and is unit-tested directly against a
log file path, independent of any CLI layer. Only the cobra command
mounting onto `cascade daemon logs` is deferred
(`// CASCADE-ALLOW: P1-E03-W1-S04-T2`, 06-FORGE-SPEC §5.19 allowed-fail
pattern) — it is not reachable from the built binary until D/S-06.T2
adds the `daemon` noun's cobra subtree. `run` / `start` / `stop` /
`restart` / `status` / `install` / `uninstall` are D/S-06.T2's surface
entirely; nothing behind them exists yet.

See also `../developer/runtime-bootstrap.md` §Logging for how the log
file this command reads is produced (slog JSON handler + size-based
rotation, wired into `internal/runtime.Bootstrap`).
