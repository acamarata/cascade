# cross-harness: the uninstall manifests

One committed manifest per adapter: the files that adapter's `Install`
actually placed, captured from a real run.
`internal/plugins/cross_harness_test.go` asserts against them.

## Provenance

| | |
|---|---|
| tool | cascade's own `cascade-codex` and `cascade-opencode` plugin adapters (`plugins/codex`, `plugins/opencode`), driven through the real generator wiring this package installs at init |
| version | the tree at the ticket's commit |
| date | 2026-09-16 |
| ticket | P1-E16-W4-S35-T4 |
| capture command | `go test ./internal/plugins/ -run TestCrossHarnessUninstallManifestGolden -update-uninstall-manifests` |
| fixture | a two-tier project: a global tier under a fake `HOME`, a repo tier under a `t.TempDir()` project |

Paths are rendered project-relative, or `$HOME/`-prefixed for the global
tier. A path outside both roots would be written verbatim — that is
precisely what a manifest is for, so it is recorded rather than hidden.

`cascade-claude` has no manifest here. Its `Uninstall` also removes hook
and MCP configuration under a `Paths` root, so "the files install placed"
is a different question for it; its own package covers that, and
conflating the two would weaken both.

## What these two files show

```
codex:      $HOME/.codex/AGENTS.md            +  AGENTS.md
opencode:   $HOME/.config/opencode/AGENTS.md  +  AGENTS.md
```

The second line is the same file. Both adapters claim it, both are right
to, and each one's uninstall will delete it.

That used to mean uninstalling either one silently de-configured the
other. `TestUninstallingOneHarnessTakesTheSharedFile` pinned it as a
recorded hazard, written to fail the day it changed. It did, and the rule
below replaced it (`P1-E16-W4-S35-T8`, R-14.265).

## The rule

**Uninstall does not remove a file another INSTALLED harness still reads.
It keeps it, and says so.**

Neither adapter can detect the other — a plugin may not import
`internal/`, so neither can ask which harnesses are installed. So the
decision is made where both sides are visible: `internal/plugins` computes
the set of paths every other installed harness generates for this project,
and passes it to uninstall. The adapter refuses to remove anything in that
set and reports the file as `Kept`, with a reason.

Three things this is NOT:

- It is not a base-name check. "Never remove a file called `AGENTS.md`"
  would refuse to remove it on a machine where only one harness was ever
  installed, littering every uninstall to protect a case that is not
  present.
- It is not a refcount on disk. That adds a persistence format, for one
  file, whose failure mode is a marker disagreeing with the filesystem.
- It is not a hardcoded "codex and opencode share AGENTS.md". The set is
  computed from what is INSTALLED and what those installs actually
  generate, so a harness that changes its instruction file name, or a
  fourth that starts sharing one, changes the answer with no edit
  anywhere.

The consequence to know about: a full uninstall of every harness removes
the file, because once the last one is gone nothing claims it. That is
covered by `TestUninstallingTheLastHarnessLeavesNothingBehind`, and it is
the half the keep rule must not trade away.

The manifest goldens beside this file are unchanged by the rule: they
record what INSTALL placed, and installing did not change.
