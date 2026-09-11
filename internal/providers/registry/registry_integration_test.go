//go:build integration

package registry

// This file's build tag is deliberate: the ticket's checks: list requires
// `go test -tags integration -run TestRegistryIntegration`, but the
// contract's files_scope does not list this file. Recorded as a
// contract/contract-checks contradiction (see this ticket's journal, both
// sides quoted) rather than silently dropping either side -- omitting the
// test would fail a checks: command the contract itself lists as
// mandatory verification; omitting the build tag would pull this test
// (and its real file-backed *sql.DB) into the default unit lane, which is
// not this test's purpose.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // the real B/S-02.T2 SQLite driver

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// TestRegistryIntegration exercises the registry against a real,
// file-backed SQLite database (not an in-memory handle): it proves
// durability by CLOSING the writing *sql.DB and opening a brand-new
// *sql.DB handle on the same file for every read, so a passing assertion
// can only be explained by the data having actually reached disk -- never
// by reading back from the same process-local connection or cache.
func TestRegistryIntegration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	ctx := context.Background()
	dialect := migrate.SQLiteEmitter{}
	clock := fakeClock{t: newTestClock().t}

	writeDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open write db: %v", err)
	}
	if err := ApplyMigrationSchema(ctx, writeDB, dialect, clock, dbPath, filepath.Join(dir, "backups")); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	writeReg := NewRegistry(writeDB, clock)

	rec := sampleProvider("integration-anthropic")
	if err := writeReg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	pool := "integration-pool"
	for _, name := range []string{"integration-a", "integration-b", "integration-c"} {
		if err := writeReg.UpsertProvider(ctx, sampleProvider(name)); err != nil {
			t.Fatalf("UpsertProvider(%s): %v", name, err)
		}
		if err := writeReg.UpsertLane(ctx, LaneRecord{
			LaneName: name, ProviderName: name, PoolMembership: pool,
			Capacity: CapacityAPICredit, State: LaneStateAvailable,
		}); err != nil {
			t.Fatalf("UpsertLane(%s): %v", name, err)
		}
	}
	if err := writeDB.Close(); err != nil {
		t.Fatalf("close write db: %v", err)
	}

	// Reopen: a fresh *sql.DB handle on the SAME file, never the one
	// that wrote the data.
	readDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer func() { _ = readDB.Close() }()
	readReg := NewRegistry(readDB, clock)

	got, err := readReg.GetProvider(ctx, "integration-anthropic")
	if err != nil {
		t.Fatalf("GetProvider after reopen: %v", err)
	}
	if got.Name != rec.Name || got.Tier != rec.Tier {
		t.Fatalf("provider did not survive reopen: got %+v", got)
	}

	lanes, err := readReg.ListPool(ctx, pool)
	if err != nil {
		t.Fatalf("ListPool after reopen: %v", err)
	}
	if len(lanes) != 3 {
		t.Fatalf("ListPool after reopen returned %d members, want 3", len(lanes))
	}

	picked, err := readReg.AdvancePoolIndex(ctx, pool)
	if err != nil {
		t.Fatalf("AdvancePoolIndex after reopen: %v", err)
	}
	if picked != "integration-a" {
		t.Fatalf("AdvancePoolIndex after reopen picked %q, want integration-a", picked)
	}
}
