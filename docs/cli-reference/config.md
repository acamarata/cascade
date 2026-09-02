# `cascade config`

Skeleton reference for the `config` noun (07-CLI-COMMAND-TREE.md §config).
The handlers documented here (`internal/runtime.ListEffectiveHandler`,
`internal/runtime.PathHandler`) are fully implemented and unit-tested
against `internal/runtime`; cobra mounting onto `cascade config ...` is a
forward-stub pending D/S-06.T1's cobra root
(`// CASCADE-ALLOW: P1-E03-W1-S04-T1`, 06-FORGE-SPEC §5.19). Until that
lands, these subcommands are not reachable from the built binary.

## `cascade config list --effective`

Renders the fully resolved configuration: every effective key, its
resolved value, and which precedence level produced it —
`default` (nobody set it) < `file` (config.toml) <
`env` (`CASCADE_<SECTION>__<KEY>`) < `flag` (a CLI flag; today this
applies to `runtime.profile` only, via `--profile`).

```
$ cascade config list --effective
elevation.allow_remote = false (default)
elevation.helper_pubkey =  (default)
logging.level = debug (file)
runtime.data_dir = /Users/me/.cascade/data (default)
runtime.home = /Users/me/.cascade (default)
runtime.profile = server (env)
schema_version = 1 (default)
```

`--json` emits the same rows as a JSON array of
`{"key", "value", "source"}` objects instead of the human table.

Sections other than `[runtime]` and `[elevation]` (logging, storage,
retrieval, ...) are listed verbatim from the file — this ticket preserves
and round-trips them but does not validate or default them; see
`runtime-bootstrap.md` §Sections this ticket owns.

## `cascade config path`

Prints the resolved filesystem/socket layout: `root`, `config`, `socket`,
`data`, `log`.

```
$ cascade config path
config = /Users/me/.cascade/config.toml
data   = /Users/me/.cascade/data
log    = /Users/me/.cascade/logs
root   = /Users/me/.cascade
socket = /Users/me/.cascade/daemon.sock
```

`--json` emits the same fields as a JSON object.

## `cascade config validate`

Not implemented by this ticket. Planned surface: load config.toml through
the same `internal/runtime.Load` path used by `list --effective` and
report validation errors without applying them. Tracked for a later
sprint; do not assume this subcommand exists until its own ticket lands.
