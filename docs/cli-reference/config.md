# `cascade config`

Reference for the `config` noun (07-CLI-COMMAND-TREE.md §config,
08-INIT-CONFIG-SPEC.md §3). All eight subcommands (`get`, `set`, `unset`,
`list`, `validate`, `edit`, `reload`, `path`) are fully implemented in
`cmd/cascade/config` (P1-E03-W1-S05-T8) and exercised end to end against a
real `*cobra.Command` tree in `cmd/cascade/config/config_test.go` +
`config_edit_test.go`.

**Mounting status:** mounted. `cmd/cascade/root.go`'s `mountConfigCmd`
attaches `config.NewConfigCmd(deps)` to the real root (see `git log --
cmd/cascade/root.go`, `bc2e09f fix(cli): mount config and extend the
exit-code rules to command groups`); `cascade config ...` is reachable
from the built `cascade` binary. This corrects a stale claim from an
earlier draft of this document (R-14 CR, P1-E03-W1-S05-T8, nit 7): the
"not reachable, no registration hook" note described a real blocker at
the time this ticket's code landed, but root.go gained the mount point
and used it before this doc was updated to match. Every example in this
file was captured from a real, standalone-mounted build
(`config.NewConfigCmd` under a throwaway root carrying root.go's own
persistent flags), behaviourally identical to the real mount, since
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
`=`-joined form; both are supported rather than picking one). `value` is
parsed as a TOML literal (`true`, `42`, `1.5`, `"text"`, `["a","b"]`); the
write is structure-preserving (comments, blank lines, and surrounding key
order in `config.toml` survive untouched, see
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
`internal/runtime.HotReloader.Reload`; see `../config-reference.md` for
the full hot-reload rules. No running daemon is not an error:

```
$ cascade config reload
no running daemon (no pidfile found); nothing to reload
```

On Windows, sending a reload signal to an actually-running daemon returns
an explicit `unsupported` (tier-2) refusal instead of silently no-op'ing,
`SIGHUP` has no Windows equivalent.

## `cascade config list --effective`

Renders the fully resolved configuration: every effective key, its
resolved value, and which precedence level produced it:
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
retrieval, ...) are listed verbatim from the file; this ticket preserves
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
round-trips unvalidated (Art.1: no invented validation for a section
this ticket does not own).

## `[hooks]` section (P1-E03-W1-S05-T1)

08-INIT-CONFIG-SPEC.md §3's `[hooks]` row: hook definitions, reload class
**hot** (R-14.9: an edit is picked up on the next reload without a daemon
restart). Each `[[hooks]]` array-of-tables entry maps to
`internal/hooks.HookConfig`:

```toml
[[hooks]]
id = "notify-on-register"       # optional, see below
trigger = "plugin.registered"   # exact-match against an event bus Kind
action_type = "plugin-call"     # "plugin-call" | "agent-note" ONLY at W1
[hooks.action_params]
plugin = "notifier"
```

| Field | Type | Notes |
|---|---|---|
| `id` | string | Stable hook identifier. If omitted, derived deterministically from `trigger` + `action_type` + a hash of `action_params`, so two config files declaring the same hook get the same id across a daemon restart without hand-assignment. |
| `trigger` | string | The event bus `Kind` string this hook fires on (exact match, no wildcard syntax at W1). |
| `action_type` | string | `plugin-call` invokes a plugin via the composition root's `PluginDispatcher` (wired to C/S-05.T7's plugin registry); `agent-note` writes a structured note via `NoteWriter` (wired once G/S-13's journal/memory domain ships). |
| `action_params` | table of string→string | Opaque to the engine: interpreted by whichever `PluginDispatcher`/`NoteWriter` implementation is wired in. |

**W1 action-type restriction (security ruling, 04 §Epic C S-05.T1):**
`shell` and any action type other than `plugin-call`/`agent-note` are
refused, at both config load/registration time and again, independently,
at dispatch time (defense-in-depth), with a `policy-denied` error. Shell
actions are deliberately NOT available yet; they land only once
I/S-18.T5's policy-routed risk ladder exists to gate them. A config file
naming `action_type = "shell"` fails validation rather than being
silently dropped or downgraded.

**Audit:** every hook fire (success, dispatch error, timeout, panic, or
refusal) publishes a `hooks.fire` event to the event bus carrying
`{hook_id, trigger, action_type, params_hash, result_code, err_msg, ts}`.
The raw `action_params` are never published, only a hash of them; an error
message that happens to echo back a secret-shaped param value is
redacted before publication. See `docs/developer/hooks.md` for the full
engine contract (timeout bound, at-least-once/idempotency, audit
guarantee) and the composition-root wiring this section's fields feed
into.

## `[plugins]` and what a plugin selection means

Two surfaces take a plugin selection, and they mean different things by it.

**`cascade init` (a fresh run) takes none.** Every plugin in the catalog is
a **builtin**: compiled into this binary, its command namespace mounted
unconditionally, with no install record, no version to pin, and no enabled
flag that anything reads. So step 4 *states* what this build ships rather
than asking about it:

```
== plugins
   [built in] cascade-claude  Cascade Claude Harness
   [built in] pbd             PBD Engine
```

A setup file may still carry a `[plugins]` table. On a fresh run it selects
nothing, and the run says so out loud rather than ignoring the key in
silence:

```toml
[plugins]
enable = ["pbd"]      # a fresh run reports: these are all built in,
                      # there is nothing to select
```

`cascade plugin list` shows the same set with `SOURCE = builtin`, and
`cascade plugin enable|disable <builtin>` refuses with that fact. All three
surfaces say the same thing.

**`cascade init --reconverge` takes a real one**, because its subject is
*installed* plugins — the ones `cascade plugin add` put on this machine,
which do have a stored enabled flag:

```
cascade init --reconverge --enable-plugin notifier --disable-plugin legacy
```

The run prints the plan and then performs it, through `cascade plugin
enable` / `cascade plugin disable`. Two rules apply:

- A plugin **you** turned on explicitly is never turned off by a blanket
  `--disable-plugin` from a setup file; the run reports it as kept.
- A toggle that fails is reported and does not fail the converge. A
  builtin named here refuses with "this plugin is built in", the config
  writes and harness regeneration still land, and the run says which
  plugin did not move.

`--check` prints the same plan and performs none of it.

## Diff-apply and managed keys (`ConfigWriter.ApplyDiff`)

`cascade config set`/`unset` write ONE key at a time. A plugin that needs
to propose several related keys at once (the first example, and as of
P1-E25-W5-S103-T1 the only one, is `cascade nself handshake` —
`docs/plugins/nself.md` § From an nSelf project) uses a different, batch seam:
`internal/runtime`'s `ConfigWriter.ApplyDiff`.

- **Canonical values only.** `set` and ApplyDiff share one literal
  validator: a value must parse as exactly ONE TOML value, and what is
  written is a canonical single-line re-encoding of that value, never the
  text you typed. `'server'  # note` is written as `"server"`; a literal
  with anything after the value (a second key, a `[table]` header on the
  next line) is refused with an invalid-literal error and nothing is
  written.
- **All-or-nothing.** Every entry in the batch is vetted first. The WHOLE
  batch is refused, and nothing is written, when any entry:
  - targets a guarded family (`elevation`, `policy`, `secrets`, `sync`,
    `nodes`, `conductor`, `agents`);
  - targets `[plugins]` outside the applying plugin's own
    `[plugins.<owner>]` table (another plugin's table, or a bare
    `[plugins]` key such as `enable_remote_runtime`);
  - targets `[plugins.<owner>].managed` directly (only ApplyDiff writes it);
  - repeats a path already in the same batch;
  - carries a secret-shaped value or inline-table key (the same check
    `set` runs, also run on each whitespace-separated part after invisible
    characters such as a zero-width space or a variation selector are
    removed, so a key split by a space, a tab, NBSP or an invisible
    character is refused) or a URL with userinfo
    (`postgres://user:password@host/...`). At a key whose name ends in
    `_dir` or `_path` (such as `project_dir`), a clean absolute path is not
    treated as an opaque token for its length alone, but a path segment
    starting with a known token prefix is refused; at any other key the
    full check applies;
  - would leave the file looser than it is now (CompareSecurity), or would
    change any key the batch did not name.
- **Ownership.** Every key ApplyDiff actually writes is recorded, in the
  same write, under the applying plugin's table as one inline table keyed
  by the full dotted path:

  ```toml
  [plugins.cascade-nself]
  managed = {"plugins.cascade-nself.postgres_db" = "\"app\"", "runtime.profile" = "\"server\""}
  ```

  This is how a second handshake tells "a value I wrote last time, safe to
  update" apart from "a value the operator set by hand, never touch".
- **`overridable = true`: a user edit is never rewritten.** If the current
  value differs from the entry AND differs from what `managed` recorded,
  the key is **skipped** with reason `user-set`. The operator's edit wins
  every time. It is never an error; that one key just does not change.
- Per entry, the outcome is exactly one of:
  - **Applied**: the key was absent, or its current value matches the
    plugin's own previously recorded value (`managed`).
  - **Unchanged**: the current value already equals the proposed one.
  - **Skipped** (`user-set`): the operator's own value is left alone.
- Authority, loosening, duplicate-path, secret-value and table-size
  refusals from ApplyDiff return `KindPolicyDenied`.
- Each `[plugins.<name>]` table is opaque to cascade and is bounded at
  64 KiB (its TOML encoding). A file with a bigger one fails to load, and
  `set`/ApplyDiff refuse to produce one.

`cascade nself handshake --json` prints exactly this shape:
`{status, applied[], unchanged[], skipped[], withheld[], missing_env[],
dsn_shape, restart_required, note}`.
