# cascade-nself

Package: `plugins/nself` · Manifest id: `cascade-nself` · Runtime: `builtin`

Reports whether a directory is an nself-managed project and whether the
`nself` CLI is reachable. It never becomes a core dependency: `internal/`
and `pkg/` import zero symbols from `plugins/nself` except the composition roots for egress and config application
(`internal/plugins/nself_wiring.go`, `internal/plugins/nself_config_wiring.go`).

### From an nSelf project (`cascade nself handshake`)

`cascade-nself` (the `cascade nself` noun) proposes the server-profile
config for a workspace that is an [nSelf](https://github.com/nself-org)
project: it reads `nself version --json` and `nself config get <KEY>`
for `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_EXTENSIONS`,
`REDIS_ENABLED`, `REDIS_PORT`, `MINIO_ENABLED`, `MINIO_PORT` and
`S3_BUCKET` — descriptors only, never a credential — and proposes them
under `[plugins.cascade-nself]`.

- `nself_add_cascade` (the MCP tool, reachable by a model or agent) always
  PROPOSES: it returns the diff and writes nothing.
- `cascade nself handshake [--dir <path>] [--json]` (the human-invoked CLI
  command) APPLIES it through `internal/runtime`'s
  `ConfigWriter.ApplyDiff` — an all-or-nothing, ownership-tracked write
  (see `docs/cli-reference/config.md` § diff-apply and managed keys).
- `runtime.profile` is set to `"server"` only when every required
  `CASCADE_STORAGE_*` env-ref (`POSTGRES_DSN`, `REDIS_URL`, `S3_ENDPOINT`, `S3_BUCKET`, `S3_KEY_ID`, `S3_SECRET`) already
  resolves in the invoking environment (`CASCADE_STORAGE_PGVECTOR_DSN` is
  optional — it falls back to `CASCADE_STORAGE_POSTGRES_DSN`); otherwise
  the handshake reports `pending-env` and names the missing env-ref
  NAMES, never a value. When `CASCADE_STORAGE_POSTGRES_DSN` is missing,
  `dsn_shape` gives the shape to export, placeholders unfilled:
  `postgres://` followed by `<user>:<password>@` and
  `<postgres_host>:<postgres_port>/<postgres_db>` (no spaces between parts).
- A project value that looks like a credential (a URL with userinfo, a
  secret-named pair, a token, a key split by whitespace or hidden by a
  zero-width character) or that the
  config writer would refuse is never proposed or written; the response
  lists its config path under `withheld`. The handshake screens every value
  with the same check `cascade config set` runs and refuses to run when no
  screen is bound.
- `project_dir` is always recorded as an absolute path, whatever `--dir`
  was given.
- A `runtime.profile` the operator already set by hand is left alone, and
  the handshake output says so ("runtime.profile is user-set;
  cascade-nself left it unchanged"); a user edit to any proposed key is
  never overwritten.

Provenance for the exact verbs and their real output:
`plugins/nself/testdata/README.md`.

> **Runtime-tier note.** The plugin was specced as `runtime = "process"`,
> `trust_tier = "trusted"`. Neither field exists in the real
> `cascade.plugin/v2` schema, and no composition root in this tree can
> launch a process-tier manifest today. T0 ruled `runtime = "builtin"` as
> the floor. That is a security downgrade, stated rather than glossed: the
> plugin runs in the daemon process with full host trust and no supervised
> child. `plugin.go`'s package doc quotes both sides.

## Tools

| Tool | Behaviour |
|---|---|
| `nself_project_info` | answers the detection question; never fails because a directory turned out not to be a project |
| `nself_add_cascade` | proposes the handshake diff and writes nothing |

## Detection heuristic

1. **Marker scan.** Walk from the scan root upward. A directory counts as a
   project when it holds a `.nself` **directory** that itself holds one of
   the files nself writes: `build-version`, `compose-files.txt`,
   `build-state`, `.first-run-complete` or `nself.yml`. The file set comes
   from real projects on a real machine (listed in `testdata/README.md`).
   - `nself.toml` is **not** a marker. No such file exists in any real
     nself project; an earlier draft invented it.
   - A `.nself` directory holding something else (a `pipelines/`
     directory, say) is not a project.
   - `$HOME/.nself` is nself's own global state directory and is refused by
     path, before any content check. Without that rule every directory
     under the home directory reads as a project.
2. **Bounds.** The walk stops at the home directory (exclusive) and at the
   first repository root (`.git`) it examines, whichever comes first, with a
   hard 64-level cap behind both. It never runs to `/` from a directory
   inside your home.
3. **Subprocess probe** (only on a marker-scan miss). Runs
   `nself status --json` — the one project-scoped verb with a real JSON
   mode — **in the scanned directory** (`cmd.Dir`), with a closed
   environment allowlist, a 2-second deadline, its own process group, and a
   group kill on the deadline. Exit 0 means detected.
4. **Every probe failure means "not detected", never an error.** A binary
   absent from `PATH`, a timeout, and a probe that *ran and exited
   non-zero* (what v1.3.5 does in any non-project directory: `no nself
   project found…`, exit 1) all fold to `detected: false`. The typed
   outcome is reported on the doctor leg as a code-chosen classification
   (`binary-absent`, `timed-out`, `ran-and-failed`, `not-runnable`).

The result is memoized per detector for a daemon session; the memo is
cleared after a config reload.

**Disclosed limit.** `nself status` exits 2 when a real project's services
are unhealthy, so the subprocess leg reports "not detected" there. The
marker scan is the primary signal and is unaffected.

## What a response can contain

```json
{
  "detected": false,
  "method": "none",
  "doctor": {
    "binary_reachable": true,
    "binary_path": "/opt/homebrew/bin/nself",
    "probe_outcome": "ran-and-failed"
  }
}
```

Project-info responses omit the subprocess's stdout and stderr:
`nself status` prints service state that can name a database URL. Two
independent layers keep it that way — the payload is BUILT from code-chosen
constants, a boolean, a directory path and this plugin's own error text;
and every string field is then passed through a credential scrub that
replaces anything shaped like a URL with userinfo or a secret-named
key/value pair. The scrub runs whatever the egress firewall would have
done. Handshake responses include the selected descriptor values only
after credential screening, and every response string passes the same scrub.

## Config fields written

Descriptors are written under `[plugins.cascade-nself]`, with ownership
recorded by full dotted path in `managed`. The CLI sets `runtime.profile`
when the required storage env-refs resolve. User edits are skipped.

## Windows

The plugin builds and its tests compile for `GOOS=windows`. Binary absence
returns a typed doctor error with a remediation hint naming the platform,
never a panic (`TestNselfPlugin_WindowsRefusal`, which injects both the
platform and the `PATH` lookup so it proves the same thing on every
machine). On Windows the probe's deadline kills the child but cannot reap a
whole process tree — a typed refusal says so rather than claiming otherwise
(`exec_windows.go`).

## Egress

Every response transits the `nself-backend` egress class
(`internal/hooks/egress/classes.go`, owner `P1-E25-W5-S52-T2`) at tier
`internal`. `internal/plugins/nself_wiring.go` binds the REAL
`egress.Engine` to the plugin's local interceptor seam, and the adapter
refuses any class other than `nself-backend`. With nothing bound — a build
without that wiring, or an operator who disabled the class — the plugin
**refuses to emit** rather than passing bytes through unfiltered.
