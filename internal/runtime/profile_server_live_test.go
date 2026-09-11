//go:build integration

// Purpose: TestServerProfileAssembly — the ticket's named CI entry point
//
//	for internal/runtime's slice of the server-profile composition:
//	proves ResolveDSNEnvRef resolves a real DSN from the environment and
//	that the PostgresMigrator closure it hands to a driver's Migrator
//	seam actually applies live against a real server. The concrete
//	Postgres/pgvector DRIVER construction is proven separately by
//	providers/postgres and providers/pgvector's own docker-tagged
//	integration tests (Art.10.2: internal/** never imports providers/**,
//	so this package cannot open a real driver itself) — this file proves
//	the piece internal/runtime actually owns.
//
// Inputs: CASCADE_TEST_POSTGRES_DSN, a real reachable Postgres server.
//
// Constraints: go:build integration only (no postgres tag: this package
//
//	imports database/sql + the pgx stdlib registration directly, the same
//	way any _test.go file may reach for a driver library the production
//	package itself never imports — see providers/postgres/integration_test.go's
//	precedent of a _test.go file reaching past its package's normal
//	import boundary).
//
// SPORT: runtime.profile_server/ADDED (P1-E17-W4-S38-T4).
package runtime

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestServerProfileAssembly is this ticket's named CI check.
func TestServerProfileAssembly(t *testing.T) {
	dsn := os.Getenv("CASCADE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CASCADE_TEST_POSTGRES_DSN not set — this lane requires a real reachable Postgres server")
	}

	getenv := func(k string) (string, bool) {
		if k == "CASCADE_TEST_ASSEMBLY_DSN" {
			return dsn, true
		}
		return "", false
	}
	resolved, err := ResolveDSNEnvRef(getenv, "CASCADE_TEST_ASSEMBLY_DSN")
	if err != nil {
		t.Fatalf("ResolveDSNEnvRef: %v", err)
	}
	if resolved != dsn {
		t.Fatalf("ResolveDSNEnvRef = %q, want %q", resolved, dsn)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", resolved)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	// Drop BOTH the data table and the migration ledger: migrate.Apply is
	// idempotent against the LEDGER, not the table (re-running it with the
	// ledger already at SchemaVersion 1 is correctly a no-op and skips the
	// CREATE TABLE step entirely) — a repeat run of this test against a
	// server that kept its ledger from a prior run, dropping only
	// cascade_server_kv, would see the DDL step silently skipped and the
	// table never recreated. Dropping applied_migrations too resets the
	// ledger so this test's own Apply call always runs the real DDL.
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS cascade_server_kv`,
		`DROP TABLE IF EXISTS applied_migrations`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("pre-test cleanup (%s): %v", stmt, err)
		}
	}

	migrator := PostgresMigrator(NewSystemClock())
	if err := migrator(ctx, db); err != nil {
		t.Fatalf("PostgresMigrator closure: %v", err)
	}
	var exists bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'cascade_server_kv')`).Scan(&exists)
	if err != nil || !exists {
		t.Fatalf("cascade_server_kv missing after PostgresMigrator: exists=%v err=%v", exists, err)
	}
}
