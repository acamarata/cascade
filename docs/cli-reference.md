# Cascade CLI Reference

This is the seed of the CLI reference. `docs gen` (AA/S-55.T6) will later
regenerate the full command tree from cobra's own metadata and use
`cmd/cascade/testdata/golden_help.txt` as its drift-detection baseline
(A-T8). Until then, this page documents the surface added by
D/S-06.T1: the root command, its global flags, `cascade version`, and
`cascade completion`.

## `cascade`

The root command. Running `cascade` with no subcommand prints usage; every
subcommand is reachable as `cascade <command> --help`.

```
Cascade is a local-first AI agent runtime: one binary that is both
the CLI surface and, via "cascade daemon run", the long-lived daemon.

Usage:
  cascade [command]
```

## Global flags

These flags are declared on the root command as persistent flags, so they
are available on every subcommand (see 07-CLI-COMMAND-TREE.md
§global-flags):

| Flag | Shorthand | Type | Description |
|---|---|---|---|
| `--json` | | bool | Emit output as a versioned JSON envelope (D/S-06.T5 output contract). |
| `--profile` | | string | Select a named config profile. |
| `--config` | | string | Override the config file path. |
| `--quiet` | `-q` | bool | Suppress progress output. |
| `--verbose` | `-v` | bool | Increase log verbosity. |
| `--no-color` | | bool | Disable colored output (also respects the `NO_COLOR` env var). Registered on the root command in `cmd/cascade/main.go`. |

`--quiet` and `--verbose` are mutually exclusive; combining them is a
usage error.

See [`docs/cli-output-contract.md`](cli-output-contract.md) for the full
output contract: the `--json` envelope's exact wire shape and version, the
NDJSON stream format used by streaming commands, the `--no-color`/`NO_COLOR`
precedence rules, and the TTY/non-TTY behavior matrix. The exit-code table
itself is owned by [`docs/reference/error-taxonomy.md`](reference/error-taxonomy.md).

## `cascade version`

Prints the build stamp: version, commit, build date, and the §D-33 install
channel (`script`, `brew`, `oci`, `node-managed`, or `manual`).

```
$ cascade version
cascade version v2.0.0
commit:  <sha>
built:   <rfc3339 timestamp>
channel: <script|brew|oci|node-managed|manual>
```

The version/commit/date fields are set at build time via `-ldflags -X`
(owned by A-T6's build tooling). A plain `go build ./cmd/cascade`, as done
in development, prints `dev` / `none` / `unknown` and channel `manual`,
since nothing stamped those variables.

## `cascade completion`

Generates a shell completion script for `bash`, `zsh`, `fish`, or
`powershell`, delegating to cobra's built-in generators.

```
$ cascade completion bash
$ cascade completion zsh
$ cascade completion fish
$ cascade completion powershell
```

### Install instructions

**bash** (current session):

```sh
source <(cascade completion bash)
```

**bash** (persistent, requires [bash-completion](https://github.com/scop/bash-completion)):

```sh
cascade completion bash > /usr/local/etc/bash_completion.d/cascade   # macOS/Homebrew
cascade completion bash > /etc/bash_completion.d/cascade             # Linux
```

**zsh** (current session):

```sh
source <(cascade completion zsh)
```

**zsh** (persistent; ensure the target directory is on `$fpath`):

```sh
cascade completion zsh > "${fpath[1]}/_cascade"
```

**fish**:

```sh
cascade completion fish | source                               # current session
cascade completion fish > ~/.config/fish/completions/cascade.fish  # persistent
```

**PowerShell**:

```powershell
cascade completion powershell | Out-String | Invoke-Expression   # current session
# add the same line to your PowerShell profile for persistence
```

## `cascade memory forget <kind>/<name>`

Retires one record: the file is unlinked after its tombstone is written,
the derived index is scrubbed, and the backup lane is told not to restore
it. Nothing prompts, so `--dry-run` is the way to look before leaping and
`--reason` records why, with the retirement, for later.

The output lists every place the record left a mark and what happened to
each, including the marks kept on purpose (the tombstone, the account, the
consolidation account) and the ones nothing here can reach (a candidate
draft, and the bytes themselves, which are unlinked and not shredded). See
`docs/memory-architecture.md` § Forget pipeline.
