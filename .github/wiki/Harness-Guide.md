# Harness Guide

A *harness* is a coding agent that reads instruction files from a
project. Cascade supports three: `claude`, `codex`, and `opencode`. This
page covers finding out which of them are on a machine, and keeping the
instruction files Cascade generates for them current.

## Listing harnesses

```
cascade context harness list [--json]
```

All three harnesses are reported every time, installed or not. That is
deliberate: "codex is not installed" and "this build does not know about
codex" are different answers, and a list that omitted the absent ones
could not tell them apart.

```
HARNESS     STATUS         DETAIL
claude      in sync        /home/you/.claude
codex       not installed  /home/you/.codex
opencode    drifted        /work/app/AGENTS.md: content differs
```

| Column | Meaning |
|---|---|
| `HARNESS` | The harness kind, in a fixed order. |
| `STATUS` | `not installed`, `in sync`, or `drifted`. |
| `DETAIL` | Why it drifted, or — for every other status — the directory that was probed. |

The `DETAIL` column names the probed directory even when nothing was
found there. A false negative is very hard to debug without knowing where
the search looked.

`--json` emits the same rows with their full fields, including the
harness version and whether Cascade is registered as an MCP server in
that harness's own configuration. Both are filled from the harness's
state file; a harness whose configuration is in a format this build does
not read reports them as absent rather than guessing.

### Where each harness is looked for

Each harness's own override environment variable is checked first, then
XDG for the one harness that honours it, then the home dotfile
directory.

| Harness | Override | Default location |
|---|---|---|
| `claude` | `CLAUDE_CONFIG_DIR` | `~/.claude` |
| `codex` | `CODEX_HOME` | `~/.codex` |
| `opencode` | — | `$XDG_CONFIG_HOME/opencode`, else `~/.config/opencode` |

The override matters more than it looks: running two accounts of one
harness side by side is exactly what it exists for, and a machine that
sets one is telling Cascade where that harness actually lives. If the
override is set, the default location is not probed at all.

Detection is read-only. It opens no database, dials nothing, writes
nothing, and works with no daemon running.

Harness detection is not available on Windows. `cascade context harness
list` there returns a structured refusal rather than an empty list —
reporting "nothing installed" for a machine nobody looked at is a false
negative someone would act on.

## Syncing instruction files

```
cascade context harness sync [--check]
```

This regenerates the instruction files for the current working directory,
or — with `--check` — reports which ones have drifted without writing
anything, exiting non-zero if any is stale.

`cascade context harness sync` and `cascade context sync` are two names
for one operation. They call one implementation, so they cannot report
differently. The shorter spelling is kept because scripts use it.

The operation is idempotent: a second run over an already-synced tree
writes nothing and exits 0.

Drift is reported for a harness that is installed. Cascade generates
instruction files for all three regardless of what is on the machine, so
drift on an absent harness is true of the file and of no use to the
reader.

## The doctor row

```
cascade doctor --harness
```

narrows a doctor run to the `harness` check alone. Without the flag, that
check runs alongside every other one, as a normal row in the report.

The check reports which harnesses are installed and whether their
instruction files are current. Harness status alone does not change
doctor's exit code — existing exit-code semantics are unchanged.

## See also

- [Context Reference](Context-Reference.md) — session scope, context
  assembly, and the tier cascade the instruction files are generated from.
