# S-32.T3 fixture provenance — pkg/plugin/storage_test.go host_storage_* fixtures

Ticket P1-E15-W4-S32-T3 (plugin storage scoping and migrations) documents
its fixture provenance here rather than appending to
`internal/storage/testdata/README.md`: that file is owned by a prior
ticket (P1-E02-W1-S03-T1) and other tickets are concurrently active in
this same batch, so editing it here would race in-flight edits — the exact
situation `T3-PROVENANCE.md` (P1-E02-W1-S03-T3) already documents and
follows in this same directory. This file should be folded into
`README.md` as a normal section the next time that file is touched by a
ticket that owns it outright.

## `pkg/plugin/storage_test.go`'s `hostStorageFixtures` table

- **Source:** a self-authored reading of this ticket's own contract text
  and `02-TARGET-STRUCTURE.md` §Storage scoping / §pkg/plugin/ — the
  `PluginStorage` interface's Get/Set/List/Delete/Migrate ABI shape. This
  is explicitly **not** an external counterpart per Art.2.1/2.4: the spec
  document and the implementation being tested share one author (this
  ticket), so passing these fixtures proves internal consistency between
  `pkg/plugin.PluginStorage`'s declared contract and its behavior, not
  agreement with an independently-authored reference.
- **Version:** the P1-E15-W4-S32-T3 ticket contract as it read on the date
  below (see `.claude/planning/p1/phase/epics/E-O/waves/W-4/sprints/S-32/
  tickets/T-3.yaml`).
- **Date:** 2026-09-07.
- **What this DOES claim:** `TestPluginStorageConformance`
  (`pkg/plugin/storage_test.go`) drives a real, fully-functional in-memory
  `PluginStorage` implementation (`referenceStorage`, defined in that same
  file — not a mock of the behavior under test) through five fixture
  operations named in the `host_storage_*` convention the N/S-30.T6
  wazero host-ABI conformance-suite skeleton establishes: set-then-get,
  get-missing-key (KindNotFound), list-by-prefix, idempotent delete, and
  idempotent migrate. `internal/storage/plugin_test.go` and
  `internal/storage/plugin_migrate_test.go` separately drive the REAL
  `internal/storage.PluginStorage`/`PluginMigrator` implementations (real
  modernc-sqlite databases, real cross-domain/sensitive-payload refusals)
  — those files are this ticket's Art.1/Art.7 real-implementation
  evidence; this file's fixtures are the ABI spec-conformance layer only.
- **What this does NOT claim:** Art.2 real-counterpart status for
  `host_storage_*`. That obligation lands in S-32.T2's conformance suite,
  which drives all three plugin runtimes (builtin/process/wasm) against
  real COMPILED guest artifacts consuming the N/S-30.T6 skeleton — an
  independently-authored counterpart this ticket's fixtures are not.
