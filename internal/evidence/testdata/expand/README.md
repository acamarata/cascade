# Provenance (Art.2.2)

This ticket (P1-E43-W9-S83-T1) exercises two real external counterparts, never a
self-authored double:

- **modernc.org/sqlite v1.58.0** (pinned in go.mod as of this ticket) -- the
  jobs_claim/jobs_claim_evidence migration (migration_claim_test.go) and every
  store test (claim_store_test.go, evidence_store_test.go) run against a real
  `*sql.DB` file under `t.TempDir()`, verified through `sqlite_master`
  directly.
- **git 2.51.0** (the `git` binary on the build machine's PATH) -- fetch_test.go
  creates a real git repository under `t.TempDir()` (`git init`, a committed
  file, then an edit past that commit) and reads it back through `git show
  <commit>:<path>` (fetch.go's `gitShow`), never a self-authored git-object
  parser.

## claim_v1_golden.json

The schema round-trip fixture (claims_test.go's
`TestClaimSchemaRoundTripsAgainstGolden`) asserting Claim and Evidence carry
exactly the R-21.41 field set as amended by R-21.94 (immutable `data_class`
on both) and R-21.86 (`captured_ref` on Evidence) -- no field added, none
omitted.

## Captured 2026-09-11.
