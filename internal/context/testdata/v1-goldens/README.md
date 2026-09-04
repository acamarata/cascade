# v1-goldens: provenance

Golden scenario fixtures for `TestDiscoverGolden` (`internal/context/discover_test.go`),
harvested per T-1's task list from v1's role-anchored tier resolver.

- tool: `git show <tag>:<path>`
- source repo: `../cascade-v1` (local clone, see `.claude/planning/p1/ARCHIVE-MAP.md`)
- tag: `archive/p9-integration`
- date harvested: 2026-09-03
- source file (pinned at the tag, byte-identical to the tag's working tree):
  `crates/cascade-core/src/resolution.rs`
  (`Resolver::discover_tier_paths`, its doc comment describing the T-P8-45
  defect and the role-anchored fix, and the `home_is_gci_only_not_also_pci`
  regression test)

## What was harvested, and how it was translated

v1's resolver used six tiers (`Gci`, `Pci`, `Apc`, `Ppc`, `Prc`, `Pac`) where
`Pci` was a *configured* personal directory, not derived from the anchor
walk. v2's tier model (this ticket) has five tiers and no configured
personal tier: `GCI`, `ASI`, `PPI`, `PRI`, `PAI`. The mapping used to
translate each v1 scenario into a v2 fixture is by ROLE, not by ordinal
position (doing it positionally is exactly the bug both resolvers exist to
avoid):

| v1 role | v1 meaning | v2 role | v2 meaning |
|---|---|---|---|
| `Gci` | home directory | `GCI` | home directory |
| `Apc` | anchor's grandparent ("projects root") | `ASI` | anchor's grandparent |
| `Ppc` | anchor's parent ("project") | `PPI` | anchor's parent |
| `Prc` | git root, or cwd fallback | `PRI` | git root, or cwd fallback |
| `Pac` | cwd, strictly below the repo root | `PAI` | cwd, strictly below the repo root |
| `Pci` | configured personal dir | — (not part of v2's five-tier model; out of scope for this ticket) | |

v1's `home_is_gci_only_not_also_pci` test is the direct ancestor of
`scenario-repo-two-levels-in-home.json` below: both assert that HOME
appears in the tier set exactly once (as GCI), and that a role whose
raw candidate directory would coincide with or overshoot HOME comes back
absent rather than duplicating GCI. v1's own doc comment for
`discover_tier_paths` ("Why the git root is the anchor... A directory is
only offered for a tier when it is DISTINCT from the tiers above it") is
the direct source for `internal/context/discover.go`'s `claimTiers` and
`boundaryFilter` helpers.

## Fixture format

Each `scenario-*.json` file describes a directory layout to build under a
fresh `t.TempDir()`, a HOME offset, a cwd offset, whether to `git init` at
the anchor offset, and the expected tier outcome for each of the five
roles. `TestDiscoverGolden` builds the layout, runs `Discover`, and asserts
byte-for-byte parity against `expect` — the tier's role, its presence, and
(when present) its directory relative to the fixture's temp root.

| Fixture | v1 scenario mirrored |
|---|---|
| `scenario-repo-directly-in-home.json` | PPI's raw candidate == HOME → dropped (single-hop version of the HOME-collision defect) |
| `scenario-repo-two-levels-in-home.json` | `home_is_gci_only_not_also_pci`: ASI's raw candidate == HOME → dropped, PPI survives |
| `scenario-unrelated-tree.json` | repo and HOME in disjoint subtrees (the common real-world case — a repo under `/Volumes/...` while HOME is under `/Users/...`) → ASI and PPI both present, normal walk |
| `scenario-app-subdir.json` | cwd strictly below the git root → PAI present alongside a normal ASI/PPI/PRI walk |
| `scenario-no-git-fallback.json` | cwd outside any git repository → PRI falls back to cwd; ASI overshoots past HOME from a different starting depth than the two HOME scenarios above and is dropped |

No file in this directory is copied v1 source — per the PRI's non-negotiable
rule, v1 designs re-enter as specs and fixtures, never as copied code.
