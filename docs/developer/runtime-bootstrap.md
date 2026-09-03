# Runtime bootstrap: profiles, paths, config, schema

Status: active from Wave 1 (P1-E03-W1-S04-T1). This documents
`internal/runtime`: the startup sequence every later subsystem's boot
path reads its paths and settings from. Ticket contract:
`.claude/planning/p1/phase/epics/E-C/waves/W-1/sprints/S-04/tickets/T-1.yaml`;
spec: `08-INIT-CONFIG-SPEC.md` §2-3.

## Profile resolution order

`internal/runtime.ResolveProfile` implements a strict four-level cascade,
first match wins:

1. `--profile` flag
2. `CASCADE_PROFILE` environment variable
3. `config.toml [runtime].profile`
4. default: `local`

An empty value at any level means "not set" and falls through to the
next. A non-empty, unrecognised value at *any* level (not just the
default) is a hard, typed `*runtime.InvalidProfileError`: the loader
never silently coerces a bad value to the default.

`Profile` is a closed enum: `local` | `server` | `worker`
(02-TARGET-STRUCTURE.md §Profiles: local and server run the same binary
with different storage/provider wiring; worker enrolls against a
controller and holds no local storage).

## Path layout

`internal/runtime.PathProvider` is the only place in the tree allowed to
call `os.UserHomeDir`: every other package receives paths through this
interface, injected, never derives them itself. Layout:

| Path | Resolution |
|---|---|
| `root` | `CASCADE_HOME` env, else `~/.cascade` |
| `config` | `CASCADE_CONFIG` env, else `root/config.toml` |
| `socket` | `CASCADE_SOCKET` env, else `root/daemon.sock` (R-14.94) |
| `data` | `root/data` |
| `log` | `root/logs` |
| per-profile storage | `root/data/storage/<profile>` |

`NewPathProvider(getenv, homeDir)` takes both accessors as parameters so
tests can inject fakes rooted at `t.TempDir()` (12-QUALITY-CONSTITUTION.md
Art.7.1: a test must never touch the real home or XDG dirs).
`NewDefaultPathProvider()` is the one production call site that binds
those parameters to the real environment; only `Bootstrap` and other
production entrypoints should call it.

## Config loading

`internal/runtime.Load` reads `config.toml` from the resolved path (a
missing file is not an error: every field falls back to its default,
and `Load` never creates the file as a side effect):

1. Decode the file into a generic tree; a parse failure is a typed
   `*runtime.ConfigError`.
2. Run the schema_version upgrade-rewrite frame (below); this may
   rewrite the file atomically.
3. Apply generic `CASCADE_<SECTION>__<KEY>` environment overrides:
   double underscore maps to a TOML dot, so
   `CASCADE_RETRIEVAL__FUSION__K=80` sets `retrieval.fusion.k`. The
   value is parsed as a TOML literal (`true`, `42`, `1.5`, `"s"`,
   `["a","b"]`); a bareword that isn't valid TOML (e.g. an unquoted
   `debug`) falls back to a plain string. `CASCADE_HOME`,
   `CASCADE_PROFILE`, `CASCADE_CONFIG`, `CASCADE_SOCKET`,
   `CASCADE_NO_INPUT`, `CASCADE_YES`, `CASCADE_TELEMETRY`, and every
   `CASCADE_INIT_*` name are reserved and never treated as a generic
   override, since they have their own dedicated resolution paths.
4. Validate the two sections this ticket owns (below).
5. Preserve every other top-level section verbatim in `Config.Extra`:
   these are valid future 08 §3 sections (`logging`, `storage`,
   `retrieval`, ...) this ticket does not own; they round-trip through a
   schema rewrite untouched and are never validated, defaulted, or
   warned about here. Inventing a default for a section this ticket does
   not own would repeat the R-14.107 mistake.

Every effective key carries a source annotation
(`default` | `file` | `env` | `flag`), retrievable via `Config.Source` or
`Config.EffectiveEntries()`, the data behind `cascade config list
--effective` (see `../cli-reference/config.md`).

### Sections this ticket owns

**`[runtime]`** (cold reload class): `profile` resolves through the
cascade above. `home` and `data_dir` are never read from the file: they
are always the resolved `PathProvider` values, stamped in by `Bootstrap`
after `Load` returns, so there is no second, independent way to
configure paths alongside `CASCADE_HOME`. An unrecognised key inside
`[runtime]` produces a warn log, never an error (08 leaves room for
later additive keys).

**`[elevation]`** (cold-only / non-hot-reloadable per 08
§[elevation]): `allow_remote` (bool) and `helper_pubkey` (string). An
unrecognised key inside `[elevation]` is a hard, typed error, unlike
`[runtime]`, this section does not tolerate silent drift, because it is
security-classified. `helper_pubkey` is required when `allow_remote` is
`true` (a missing-required-field typed error naming the field). This
ticket loads and shape-validates the section only: tightening-only
enforcement and divergent-boot detection are C/S-05.T8's
(`baseline.go`), and this package has no dependency on the vault or
daemon trust store.

## schema_version upgrade-rewrite frame

`internal/runtime.UpgradeSchema` detects a config tree's current
`schema_version` (0 when the key is absent, the legacy/unversioned
case) and runs every applicable migration step up to
`CurrentSchemaVersion`, mutating the tree in place. `Load` calls it
whenever a config file exists, and atomically rewrites the file (temp
file in the same directory + rename) only when something actually
changed.

The frame is idempotent by construction: a second run against an
already-current tree performs zero steps and reports `Mutated=false`
without touching the file. A `schema_version` newer than the binary
understands is a hard `*runtime.SchemaError`: downgrade is never
attempted.

`migrationSteps` is empty today: schema 1 is the only generation that
has ever shipped, so the only migration this ticket needs (v0, no key
present, to 1) is the version stamp itself. A later ticket appends a
real transform to `migrationSteps` the day schema 2 needs one.

## Clock injection

Every domain type in this package reads time through `runtime.Clock`,
never `time.Now()` directly (02-TARGET-STRUCTURE.md §v1.1; R-14.11 makes
`internal/runtime` the canonical home for this interface; there is no
`pkg/clock`). Production code uses `NewSystemClock()`; tests must use
`NewFixedClock` so time-dependent assertions stay deterministic.

## Logging (P1-E03-W1-S04-T2)

`internal/runtime.Config.Logging` is the typed view of `[logging]` (08
§3, hot reload class: the whole section is live-reconfigurable, no
`config.restart.required` is ever emitted for these keys):

| Key | Type | Default | Notes |
|---|---|---|---|
| `logging.level` | `debug\|info\|warn\|error` | `info` | unrecognised value is a typed `*runtime.ConfigError` |
| `logging.format` | `json\|text` | `json` | unrecognised value is a typed `*runtime.ConfigError` |
| `logging.rotation.max_size_mb` | positive int | **none** | see below |
| `logging.rotation.max_files` | positive int | **none** | see below |

**R-14.107 is binding:** the rotation keys have no numeric default.
Rotation stays fully disabled (the log file grows forever) unless
`max_size_mb` AND `max_files` are both explicitly set in `config.toml`.
`internal/runtime.loggingRotation.Enabled()` is the single source of
truth for this check; nothing in this ticket invents a fallback number.
An unrecognised key inside `[logging.rotation]` is a hard typed error
(the table is small and closed); an unrecognised key at the top level of
`[logging]` only warns, matching `[runtime]`'s additive-safe tolerance;
08 leaves room for later keys under `[logging]` itself.

`internal/runtime.NewLogger` wraps `log/slog`: a JSON handler by
default, a text handler when `format = "text"`, at the configured
level, constructor-injected everywhere, never `slog.SetDefault`.
`Logger.SetLevel(slog.Level)` updates the active handler's minimum level
without rebuilding it, for C/S-05.T8's hot-reload engine.

`internal/runtime.RotatingWriter` (rotation.go) is the size-based
rotation writer behind the log file: it wraps `PathProvider`'s log
directory (`internal/runtime.LogFilePath`, since `PathProvider` itself
has no dedicated `LogPath()`; deriving it in logger.go avoided touching
paths.go, which this ticket's `files_scope` does not name), rotates via
`os.Rename` when a write would cross `MaxSizeMB` (numbered backups
`<path>.1` newest .. `<path>.MaxFiles` oldest, oldest pruned first), and
emits one JSON line into the fresh active file naming the rotated path
and its sequence number. `Reconfigure(maxSizeMB, maxFiles int)` updates
the thresholds live; `<=0` for either disables rotation, mirroring
R-14.107's "both or neither" semantics on a hot reload. All file
operations serialise under one mutex, so concurrent writers never
produce a torn line. No CGO, no platform-specific syscalls: `os.Rename`
is available on every platform this ticket targets, including the
windows/amd64 tier-2 build.

`internal/runtime.LogProvider` (logger.go) is the constructor-injected
pairing Bootstrap builds from `Config.Logging` and `PathProvider`:
`Logger()` returns the `*slog.Logger`, `SetLevel`/`Reconfigure` forward
to the wrapped `Logger`/`RotatingWriter` for hot-reload, and `Close()`
flushes and closes the log file.

## `cascade daemon logs [-f]` (P1-E03-W1-S04-T2)

`internal/runtime.DaemonLogsHandler` (daemon_logs.go) reads the log file
at `LogFilePath(paths)` and writes it to `stdout` (diagnostics; a
missing or, in follow mode, disappeared file, go to `stderr`, per
D/S-06.T5's output contract). It never requires a live daemon: reading
the file is all it does. `-f` polls via `os.Stat` (no inotify/FSEvents
dependency) and streams new lines until the caller's context is
cancelled. See `../cli-reference/daemon.md` for the CLI-facing
description; cobra mounting is a forward-stub pending D/S-06.T2 (same
allowed-fail pattern as `cascade config`'s handlers, 06-FORGE-SPEC
§5.19).

## Bootstrap

`internal/runtime.Bootstrap` is the composed entrypoint: it resolves
`PathProvider`, calls `Load` against the resolved config path, stamps
`Config.Runtime.Home`/`DataDir` from the resolved paths, constructs the
`LogProvider` described above from `Config.Logging` and `PathProvider`,
and returns a `*Runtime` holding the active `Profile`, `PathProvider`,
`*Config`, `Clock`, and `Log`. `Runtime.Close()` closes the log file (a
nil `*Runtime` or nil `Log` is a safe no-op). It is the startup-sequence
anchor later tickets (S-05.T3, S-05.T4) extend by wiring in additional
`change:` entries. They must not construct `PathProvider`, `Config`, or
`LogProvider` themselves.
