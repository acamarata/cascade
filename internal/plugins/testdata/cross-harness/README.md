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

`TestUninstallingOneHarnessTakesTheSharedFile` pins that behaviour as a
**recorded hazard**: uninstalling one of the two silently de-configures
the other. Neither adapter can currently detect the other, because a
plugin may not import `internal/`, so neither can ask which harnesses are
installed. The test is written to SKIP with an explanation once the
behaviour changes, so whoever fixes it is told to rewrite the test rather
than finding a stale assertion in their way.

Tracked as `P1-E16-W4-S35-T8` (R-14.254).
