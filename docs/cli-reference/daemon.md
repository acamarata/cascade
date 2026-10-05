# `cascade daemon`

Reference for the `daemon` noun (07-CLI-COMMAND-TREE.md §daemon). The
lifecycle verbs `run`, `start`, `stop`, `restart`, `status`, `install` and
`uninstall` are mounted next to `logs`, which this page documents.

## `cascade daemon logs [-f]`

Reads the single log file every daemon subsystem writes to
(`internal/runtime.LogFilePath`, under `PathProvider.LogDir()`) and
prints it to stdout. **Does not require a live daemon process**: the
command reads the file directly; if the daemon has never run, it prints
a diagnostic to stderr and exits cleanly rather than erroring.

```
$ cascade daemon logs
{"time":"2026-09-02T10:00:00Z","level":"INFO","msg":"daemon started"}
{"time":"2026-09-02T10:00:01Z","level":"INFO","msg":"provider registered","name":"claude"}
```

`-f` (follow) keeps streaming new lines as they are appended, polling via
`os.Stat` (no inotify/FSEvents dependency), until interrupted:

```
$ cascade daemon logs -f
...
```

Following survives a log rotation. When the active file is renamed away
and a fresh one is created at the same path, the command prints a
`rotated out from under the reader` diagnostic to stderr and carries on
with the new file, reading it from its first line, so every line of the
rotated-in file appears and later lines keep arriving. Rotation is a
rename followed by a create, so the path is briefly absent; the poll loop
waits a few intervals before it calls the file deleted.

If the log file is deleted and does not come back, the command prints a
`disappeared` diagnostic to stderr and exits cleanly; this is not treated
as a failure. Interrupting the command (Ctrl-C) also exits cleanly.

Output contract: log content goes to **stdout**; diagnostics
(missing file, disappeared file, rotation) go to **stderr**. The command
never prints the "running in embedded (daemonless) mode" notice: reading a
file has no embedded fallback to announce.

`internal/runtime.DaemonLogsHandler` holds the read and follow logic and is
unit-tested directly against a log file path. The command wraps it in a loop
that re-runs the handler after a rotation.

See also `../developer/runtime-bootstrap.md` §Logging for how the log
file this command reads is produced (slog JSON handler + size-based
rotation, wired into `internal/runtime.Bootstrap`).

## Config errors at startup

`cascade daemon run` (and `start`, `stop`, `restart`, `status`) load
`config.toml` first. A load error keeps its own kind: a config file that
the permission policy refuses exits as `permission_denied`, not as
`invalid_input`. Only an error with no kind of its own, such as malformed
TOML, is classified `invalid_input`. See `config.md` for the policy.
