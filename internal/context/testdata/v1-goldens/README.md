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

---

# merge/: provenance for the instruction-merge fixtures (T-2)

Golden fixtures for `TestMergeGolden` and its siblings in
`internal/context/merge_golden_test.go`, harvested per T-2's task list and
R-14.17 from v1's own tier instruction files.

- tool: `git show <tag>:<path>`
- source repo: `../cascade-v1` (local clone, see `.claude/planning/p1/ARCHIVE-MAP.md`)
- tag: `archive/p9-integration`
- date harvested: 2026-09-04
- source files (pinned at the tag, byte-identical to the tag's working tree):
  `templates/cascade-defaults/{gci,apc,ppc,prc,pac}/CASCADE.md`

## Inputs: v1-harvested, verbatim

| Fixture | v1 source file | v1 role | v2 role |
|---|---|---|---|
| `tier-gci.md` | `templates/cascade-defaults/gci/CASCADE.md` | `Gci` | GCI |
| `tier-asi.md` | `templates/cascade-defaults/apc/CASCADE.md` | `Apc` | ASI |
| `tier-ppi.md` | `templates/cascade-defaults/ppc/CASCADE.md` | `Ppc` | PPI |
| `tier-pri.md` | `templates/cascade-defaults/prc/CASCADE.md` | `Prc` | PRI |
| `tier-pai.md` | `templates/cascade-defaults/pac/CASCADE.md` | `Pac` | PAI |

The mapping is BY ROLE, using the same table this README establishes for
T-1's discovery fixtures; v1's `Pci` tier has no v2 counterpart and is not
harvested. The five files are copied byte-for-byte and are not edited: they
are real instruction files a real tool shipped, which is why they carry a
real conflict (`## Cascade Position` is defined by four of the five tiers)
and a real near-miss (`## Master Lists` in PPI versus `## Master List` in
PRI, which must NOT collapse).

## Expectation: derived from the SPEC, not from this implementation

`expected-merge.txt` was written by hand from R-14.15's precedence words and
R-14.16's section grammar, by reading each harvested file's `## ` headings in
document order and applying the rule on paper. It was NOT generated from
`MergeTiers`' output: a golden regenerated from the code it checks passes
just as happily with the precedence inverted. Verified by mutation --
inverting the tier iteration order in `merge.go` turns `TestMergeGolden` red.

## What v1's own merge did, and where v2 deliberately diverges

R-14.17 calls for harvesting v1-generated `CLAUDE.md` OUTPUTS alongside the
tier files that produced them. **No such generated artifact is preserved in
the archive**: the v1 clone contains no rendered output carrying the
`<!-- cascade-tier: ... -->` marker its generator emits, so there is nothing
to copy. v1's emission behaviour is therefore recorded from its source and
its own pinned test instead:

- `crates/cascade-core/src/cascade_resolution/full.rs`
  (`build_merged_instructions`) concatenates every found tier's text,
  GCI-first, each block prefixed with a tier marker.
- `crates/cascade-core/tests/tier_distinction.rs`
  (`instruction_text_is_merged_most_general_first_and_nothing_is_dropped`)
  pins two invariants: most-general-first ordering, and **no tier's text is
  dropped**.

v2 keeps the ordering invariant exactly. It NARROWS the second one, and the
narrowing is what this ticket exists for: v1 emitted a lower tier's
contradicting section alongside the higher tier's, leaving the reader to
choose. R-14.15/R-14.16 resolve the contradiction instead, which means a
block CAN now be dropped, but only when a strictly higher tier defined the
same heading. `TestMergeGoldenPreservesV1Invariants` asserts exactly that
bound so it cannot widen into unexplained loss. R-14.16's oracle clause
("golden parity wins over this prose") does not settle the divergence,
because v1 never had a section-merge to be an oracle for; it is recorded
here and in the ticket journal rather than papered over.

## The pre-heading block

The ratified grammar keys sections by their `## ` heading. Content BEFORE a
file's first heading has no heading, so it is not conflict-keyed: every
tier's preamble survives, at its own tier's position. The alternative --
keying every preamble as the empty string so they all compete -- would delete
four of the five files' titles and front matter, which is the silent-override
failure the merge model exists to prevent. Recorded as a deliberate reading
of a case the ruling does not name.

---

# cc/: provenance for the CC harness instruction fixtures (T-3)

Expected-output fixtures for `TestCCGolden` in `internal/context/gen_cc_test.go`,
covering `CCInstructionWriter.Generate`.

- tool: `git show <tag>:<path>`
- source repo: `../cascade-v1` (local clone, see `.claude/planning/p1/ARCHIVE-MAP.md`)
- tag: `archive/p9-integration`
- date harvested: 2026-09-05
- source files (pinned at the tag, byte-identical to the tag's working tree):
  - `crates/cascade-cli/src/cmd/generate_instructions/cc.rs`
    (`render_cc_header`, `CASCADE_HEADER_MARKER`, `CASCADE_HEADER_CLOSE`) --
    the emitted block's exact wording, marker strings and layout
  - `crates/cascade-cli/src/cmd/generate_instructions/tests.rs`
    (`cc_claude_md_written`, `cc_idempotent`, `agents_md_is_symlink`) --
    the behaviour the emitted file had to satisfy
  - `crates/cascade-types/src/cascade_tier.rs` (`description`) -- the
    parenthesised tier description in the header line
- tier inputs: the five `../merge/tier-*.md` files already harvested for T-2
  (their own provenance is in the section above)

## Inputs: derived from harvested tiers, not hand-written

The `MergedContext` each fixture is rendered from is produced by running the
real S-08.T1/T2 pipeline (`MergeTiers`) over the five harvested `tier-*.md`
files, exactly as `TestCCGolden` does at run time. Nothing about the input is
invented here.

## Outputs: assembled from v1's format and the hand-written merge manifest

R-14.17 calls for harvesting v1-generated `CLAUDE.md` OUTPUTS. The section
above records why none exists in the archive: **the v1 clone preserves no
rendered output carrying the generator's marker**, only the generator and its
tests. That finding still holds for this ticket, and the same honest
substitute is used: the expected outputs were assembled from two sources that
are both independent of `CCInstructionWriter`.

1. The BLOCK FORMAT is v1's, read off `render_cc_header` at the pinned tag:
   the opening and closing marker strings verbatim, the
   `## Cascade Context — <ROLE> Tier (<description>)` heading line, the
   `**MCP server:**` line, and the two `cascade.search` /
   `cascade.context_slice` call lines in that order.
2. WHICH SECTIONS each tier's file carries comes from `../merge/expected-merge.txt`,
   which was itself written by hand from R-14.15 and R-14.16 rather than from
   any implementation. Each fixture's body is that tier's surviving sections,
   in manifest order, taken verbatim from its harvested tier file.

The assembly was done by a throwaway script implementing the section grammar
a second time, from the ruling's words, and cross-checked against the
manifest: a heading in the manifest that the second splitter could not find
was a hard error rather than a silent omission. No byte of any fixture came
out of `internal/context`.

Verified by mutation: renaming `cascade.search` to `cascade.lookup` in the
renderer turns all five fixtures red, and deleting the emission-order sort in
`groupByRole` turns `TestCCGoldenOrderIsMostGeneralFirst` red.

## The two v2 deltas from v1's format

1. **The CLI fallback line** (ratified by 18-T0-RULINGS-R16 R-16.43). Every
   generated block ends its header with:

   > If the cascade MCP tools are unavailable, run `cascade recall` and
   > `cascade context slice` through Bash instead.

   v1's post-mortem found the MCP server could be dead while the instruction
   still told the harness to call it, so the harness did nothing at all. This
   line makes an outage a degradation instead of a silent no-op. It is the
   only wording change from the harvested header.

2. **A digest attribute on the opening marker.** v1's marker was a bare
   `<!-- cascade:generate-instructions -->` and its idempotency check was
   "is the marker present", which cannot tell a regenerated block from one a
   user edited afterwards. v2 records `digest=sha256:<hex>` of the block's
   body, so a hand edit is detected and the write refuses (or backs up)
   rather than destroying it. A v1 marker with no digest attribute is treated
   as intact, so upgrading a v1-written file still works.

   The fixtures carry the literal `<DIGEST>` placeholder in that position.
   `loadCCGolden` computes the real hash with `crypto/sha256` directly rather
   than calling the package's own `bodyDigest`, so a broken digest helper
   cannot make the golden agree with it.

## Fixture format

One file per tier, named `<role>.CLAUDE.md`, holding the exact bytes
`Generate` must return for that tier's `HarnessFile.Content`.

| Fixture | tier | v1 source tier file |
|---|---|---|
| `gci.CLAUDE.md` | GCI | `../merge/tier-gci.md` |
| `asi.CLAUDE.md` | ASI | `../merge/tier-asi.md` |
| `ppi.CLAUDE.md` | PPI | `../merge/tier-ppi.md` |
| `pri.CLAUDE.md` | PRI | `../merge/tier-pri.md` |
| `pai.CLAUDE.md` | PAI | `../merge/tier-pai.md` |

## What v1 did that v2 does not

v1's `generate_cc` also created an `AGENTS.md` symlink and rewrote
`settings.json` for MCP registration. Neither is part of this ticket's
generator seam: the first belongs to the OpenCode/Codex generators (S-09.T3),
the second to MCP registration, and both are outside `files_scope` here.
Recorded so the omission reads as scope rather than loss.
