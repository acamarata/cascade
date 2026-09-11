# Fixture provenance — TestEpicMAcceptance / journal RPC tests

Art.2 provenance record for `epicm_acceptance_test.go`'s real-counterpart
fixtures (P1-E13-W3-S27-T4).

- **Go client SDK (D/S-07.T3):** `internal/client.Client`, in-tree at the
  same commit as this file (this package imports it as a normal module
  dependency, not a vendored snapshot — there is no separate SDK version
  to pin).
- **Daemon:** `internal/daemon.NewRPCServer` + `internal/rpc.Registry`,
  in-tree at the same commit as this file. The acceptance test stands up
  a real instance of both directly (see `epicm_acceptance_test.go`'s own
  doc comment for why: the production composition root,
  `cmd/cascade/daemon_unix_run.go`'s `buildRPCServer`, is outside this
  ticket's `files_scope`).
- **Storage:** `internal/storage/storetest.NewMemStore()` — an in-memory
  `provider.Store` double, not the on-disk SQLite driver
  (`providers/sqlite`). `internal/fleet/journal/store_test.go` already
  covers the real SQLite driver's storage-layer correctness (torn-tail
  recovery, transactional commit); this test's subject is the RPC/daemon
  layer above it, for which a real socket and a real registry are the
  parts that matter.
- **Go toolchain:** go1.26.6 darwin/arm64 (`go version` at the time this
  file was written).
- **Date captured:** 2026-09-11.

No golden byte fixtures are checked in here: every entry this test
exercises is seeded at run time through `journal.SQLiteStore.Append` (the
real write path, T1's own scope) and read back through the real RPC round
trip, so there is nothing to snapshot that the test does not already
construct and verify structurally.
