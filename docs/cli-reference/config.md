# `cascade config`

Reference for the `config` noun (07-CLI-COMMAND-TREE.md §config,
08-INIT-CONFIG-SPEC.md §3). All eight subcommands — `get`, `set`, `unset`,
`list`, `validate`, `edit`, `reload`, `path` — are fully implemented in
`cmd/cascade/config` (P1-E03-W1-S05-T8) and exercised end to end against a
real `*cobra.Command` tree in `cmd/cascade/config/config_test.go` +
`config_edit_test.go`.

**Mounting status:** mounted. `cmd/cascade/root.go`'s `mountConfigCmd`
attaches `config.NewConfigCmd(deps)` to the real root (see `git log --
cmd/cascade/root.go`, `bc2e09f fix(cli): mount config and extend the
exit-code rules to command groups`) — `cascade config ...` is reachable
from the built `cascade` binary. This corrects a stale claim from an
earlier draft of this document (R-14 CR, P1-E03-W1-S05-T8, nit 7): the
"not reachable, no registration hook" note described a real blocker at
the time this ticket's code landed, but root.go gained the mount point
and used it before this doc was updated to match. Every example in this
file was captured from a real, standalone-mounted build
(`config.NewConfigCmd` under a throwaway root carrying root.go's own
persistent flags) — behaviourally identical to the real mount, since
`mountConfigCmd` passes the same `Deps` shape.

## `cascade config get <key>`

Prints one key's resolved (effective) value.

```
$ cascade config get logging.level
logging.level = debug (file)
```

`--json` emits `{"key","value","source"}`. An unknown key exits
`not-found` (exit 3) with a nearest-match suggestion (from `set`'s own
dotted-path resolver).

## `cascade config set <key> <value>` / `<key>=<value>`

Both call shapes are accepted (07-CLI-COMMAND-TREE.md uses the
space-separated form; this ticket's own acceptance criteria use the
`=`-joined form — both are supported rather than picking one). `value` is
parsed as a TOML literal (`true`, `42`, `1.5`, `"text"`, `["a","b"]`); the
write is structure-preserving (comments, blank lines, and surrounding key
order in `config.toml` survive untouched — see
`../config-reference.md` §Round-trip fidelity) and validate-before-write
(disk is never touched if the resulting file would fail `config validate`).

```
$ cascade config set retrieval.fusion.k=80
retrieval.fusion.k = 80
```

A secret-shaped value (bearer-token prefix, PEM header, bare high-entropy
token >=40 chars) is refused and redirected:

```
$ cascade config set registry.pubkey_path="ghp_abcdefghijklmnopqrstuvwxyz0123456789"
Error: invalid-input: config set registry.pubkey_path: runtime: config set
registry.pubkey_path: value looks like a secret (matches known bearer-token
prefix "ghp_"); use `cascade vault set` instead
```

An unknown key is refused with a nearest-match suggestion and no write:

```
$ cascade config set totally.unknown.key=1
Error: invalid-input: config set totally.unknown.key: runtime: config key
"totally.unknown.key": unknown config key (did you mean "daemon.socket"?)
```

## `cascade config unset <key>`

Removes `key`'s line, reverting it to its schema default on next load. An
already-unset key is a no-op, not an error.

```
$ cascade config unset logging.level
logging.level removed (now reverts to its schema default)
```

## `cascade config edit`

Opens `$EDITOR` (falling back to `vi`) on `config.toml`. On save, the
edited content is decoded and run through `config validate`'s same check;
an invalid result is refused and `config.toml` is left byte-for-byte
unchanged. A valid result is written atomically and triggers the same
best-effort daemon-reload notification `set`/`unset` use.

```
$ EDITOR=vim cascade config edit
config.toml updated and validated
```

## `cascade config reload`

Sends the running daemon `SIGHUP` (found via its pidfile), which triggers
`internal/runtime.HotReloader.Reload` — see `../config-reference.md` for
the full hot-reload rules. No running daemon is not an error:

```
$ cascade config reload
no running daemon (no pidfile found); nothing to reload
```

On Windows, sending a reload signal to an actually-running daemon returns
an explicit `unsupported` (tier-2) refusal instead of silently no-op'ing —
`SIGHUP` has no Windows equivalent.

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

Decodes `config.toml` and runs `internal/runtime.Validate` (the same
check every write path in `set`/`unset`/`edit`/hot-reload runs
before touching disk) without applying anything.

```
$ cascade config validate
config.toml is valid
```

An invalid file (malformed TOML, `[elevation]`/`[logging]` shape errors,
or a `schema_version` newer than this binary supports) exits
`invalid-input` (exit 2) naming the offending field.

`Validate` type-checks only the two sections this repo owns
([runtime], [elevation]) plus [logging]; every other 08 §3 section
round-trips unvalidated (Art.1 — no invented validation for a section
this ticket does not own).

## `[hooks]` section (P1-E03-W1-S05-T1)

08-INIT-CONFIG-SPEC.md §3's `[hooks]` row: hook definitions, reload class
**hot** (R-14.9 — an edit is picked up on the next reload without a daemon
restart). Each `[[hooks]]` array-of-tables entry maps to
`internal/hooks.HookConfig`:

```toml
[[hooks]]
id = "notify-on-register"       # optional — see below
trigger = "plugin.registered"   # exact-match against an event bus Kind
action_type = "plugin-call"     # "plugin-call" | "agent-note" ONLY at W1
[hooks.action_params]
plugin = "notifier"
```

| Field | Type | Notes |
|---|---|---|
| `id` | string | Stable hook identifier. If omitted, derived deterministically from `trigger` + `action_type` + a hash of `action_params`, so two config files declaring the same hook get the same id across a daemon restart without hand-assignment. |
| `trigger` | string | The event bus `Kind` string this hook fires on (exact match — no wildcard syntax at W1). |
| `action_type` | string | `plugin-call` invokes a plugin via the composition root's `PluginDispatcher` (wired to C/S-05.T7's plugin registry); `agent-note` writes a structured note via `NoteWriter` (wired once G/S-13's journal/memory domain ships). |
| `action_params` | table of string→string | Opaque to the engine — interpreted by whichever `PluginDispatcher`/`NoteWriter` implementation is wired in. |

**W1 action-type restriction (security ruling, 04 §Epic C S-05.T1):**
`shell` and any action type other than `plugin-call`/`agent-note` are
refused — at both config load/registration time and again, independently,
at dispatch time (defense-in-depth) — with a `policy-denied` error. Shell
actions are deliberately NOT available yet; they land only once
I/S-18.T5's policy-routed risk ladder exists to gate them. A config file
naming `action_type = "shell"` fails validation rather than being
silently dropped or downgraded.

**Audit:** every hook fire — success, dispatch error, timeout, panic, or
refusal — publishes a `hooks.fire` event to the event bus carrying
`{hook_id, trigger, action_type, params_hash, result_code, err_msg, ts}`.
The raw `action_params` are never published, only a hash of them; an error
message that happens to echo back a secret-shaped param value is
redacted before publication. See `docs/developer/hooks.md` for the full
engine contract (timeout bound, at-least-once/idempotency, audit
guarantee) and the composition-root wiring this section's fields feed
into.
