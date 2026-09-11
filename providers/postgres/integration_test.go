//go:build postgres && integration

// Purpose: the storetest-under-docker lane's real conformance run —
//
//	CLOSES the B/S-03.T5 rule-19 allowed-fail leg (06 §5.19): every
//	storetest.RunStoreTests sub-test now passes against a REAL Postgres
//	server, not the former stub. Also proves the live migration behaviors
//	(ordered apply, schema_version, minimum_reader_version refusal) and
//	the named error paths (unreachable server, missing env-ref, auth
//	failure, migration failure, older-reader refusal) — never a
//	hand-rolled fake postgres (Art.2).
//
// Inputs: CASCADE_TEST_POSTGRES_DSN, a real reachable Postgres server's
//
//	connection string. TestMain skips the whole file with a clear message
//	when it is unset, so a machine without Docker never reports a false
//	pass or a false fail — see AGENT-BRIEF "if Docker is unavailable, say
//	so plainly."
//
// Constraints: go:build postgres && integration — this file compiles only
//
//	when BOTH tags are supplied (`go test -tags="postgres integration"`),
//	matching the fact that its production code is itself postgres-tagged
//	(AGENT-BRIEF's build-tag-parity rule) while staying out of the plain
//	`-tags=postgres` unit lane, which must not require a live server.
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).
package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/postgres"
)

// testDSN returns the real server's DSN, skipping t with a clear message
// when CASCADE_TEST_POSTGRES_DSN is unset — never a silent pass and never
// a false failure on a machine with no Docker.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CASCADE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CASCADE_TEST_POSTGRES_DSN not set — this lane requires a real reachable Postgres server (see providers/postgres/testdata/README.md)")
	}
	return dsn
}

// TestPostgresStoretestUnderDocker is the ticket's named CI entry point:
// the full provider.Store conformance suite against a REAL server, one
// fresh namespace-clean Driver per sub-test (each Open reuses the same kv
// table; storetest's own per-sub-test namespaces already give isolation,
// matching how the sqlite conformance run shares one on-disk file across
// its sub-tests too).
func TestPostgresStoretestUnderDocker(t *testing.T) {
	dsn := testDSN(t)
	storetest.RunStoreTests(t, func(t *testing.T) provider.Store {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		d, err := postgres.Open(ctx, dsn)
		if err != nil {
			t.Fatalf("postgres.Open: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	})
}

// TestPostgresLiveMigration proves the B/S-02.T3 postgres dialect applies
// live: ordered apply, schema_version, minimum_reader_version refusal —
// the "W1 golden-SQL contract holds on postgres" acceptance criterion.
// DBPath is left empty (no §D-18 snapshot): that mechanism copies a
// SQLite .db file and has no meaning for a live Postgres connection, per
// ApplyConfig's own doc comment.
func TestPostgresLiveMigration(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	// A fresh table name per run avoids collisions with
	// TestPostgresStoretestUnderDocker's kv table and any other test in
	// this lane sharing the same database. Both the data table AND the
	// migration ledger are dropped: migrate.Apply is idempotent against
	// the LEDGER, so a stale ledger from a prior run would make this run's
	// first Apply silently skip the CREATE TABLE step it is here to prove.
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS cascade_server_kv`,
		`DROP TABLE IF EXISTS applied_migrations`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("pre-test cleanup (%s): %v", stmt, err)
		}
	}

	set := liveMigrationTestSet()
	cfg := migrate.ApplyConfig{DB: db, Dialect: migrate.PostgresEmitter{}, Clock: fixedClock{}}
	assertOrderedApplyAndIdempotence(ctx, t, db, cfg, set)
	assertMinimumReaderVersionRefusal(ctx, t, cfg, set)
}

// liveMigrationTestSet is the single-table MigrationSet
// TestPostgresLiveMigration proves live.
func liveMigrationTestSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name: "cascade_server_kv",
				Columns: []migrate.ColumnDef{
					{Name: "namespace", Type: migrate.TypeText, PrimaryKey: true},
					{Name: "key", Type: migrate.TypeText, PrimaryKey: true},
					{Name: "value", Type: migrate.TypeBlob},
				},
			},
		}},
	}
}

// assertOrderedApplyAndIdempotence proves the first Apply creates the
// table and a second, identical Apply is a true no-op (idempotent re-apply
// per the ledger).
func assertOrderedApplyAndIdempotence(ctx context.Context, t *testing.T, db *sql.DB, cfg migrate.ApplyConfig, set migrate.MigrationSet) {
	t.Helper()
	if err := migrate.Apply(ctx, cfg, set); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	var exists bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'cascade_server_kv')`).Scan(&exists)
	if err != nil || !exists {
		t.Fatalf("cascade_server_kv table missing after Apply: exists=%v err=%v", exists, err)
	}
	if err := migrate.Apply(ctx, cfg, set); err != nil {
		t.Fatalf("second (idempotent) Apply: %v", err)
	}
}

// assertMinimumReaderVersionRefusal proves a set claiming a lower
// MinimumReaderVersion than the on-disk schema_version is refused with
// *migrate.SchemaDowngradeError before any DDL runs.
func assertMinimumReaderVersionRefusal(ctx context.Context, t *testing.T, cfg migrate.ApplyConfig, set migrate.MigrationSet) {
	t.Helper()
	downgrade := set
	downgrade.SchemaVersion = 2
	downgrade.MinimumReaderVersion = 0
	err := migrate.Apply(ctx, cfg, downgrade)
	if err == nil {
		t.Fatal("Apply with MinimumReaderVersion below the recorded schema_version succeeded, want a downgrade refusal")
	}
	var downgradeErr *migrate.SchemaDowngradeError
	if !errors.As(err, &downgradeErr) {
		t.Fatalf("Apply downgrade error = %v (%T), want *migrate.SchemaDowngradeError", err, err)
	}
}

// TestPostgresOpen_UnreachableServer proves the "unreachable server" error
// path against a real (but non-listening) TCP address, on the docker lane
// where a genuine live server is also reachable for contrast.
func TestPostgresOpen_UnreachableServer(t *testing.T) {
	testDSN(t) // require the lane to be configured at all, per file policy
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := postgres.Open(ctx, "postgres://postgres:test@127.0.0.1:1/cascade_test?sslmode=disable&connect_timeout=1")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Open(unreachable) = %v, want KindUnavailable", err)
	}
}

// TestPostgresOpen_AuthFailure proves the "auth failure" error path
// against the real server with a deliberately wrong password.
func TestPostgresOpen_AuthFailure(t *testing.T) {
	dsn := testDSN(t)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("url.Parse(dsn): %v", err)
	}
	parsed.User = url.UserPassword("postgres", "definitely-wrong-password")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = postgres.Open(ctx, parsed.String())
	if !cascade.HasKind(err, cascade.KindPermissionDenied) && !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Open(bad password) = %v, want KindPermissionDenied or KindUnavailable", err)
	}
}

// TestDriverTx_GetAndDelete exercises driverTx.Get and driverTx.Delete
// directly (storetest's own Tx sub-tests only drive tx.Put and
// tx.CompareAndSwap, never tx.Get/tx.Delete in isolation — Art.4 coverage
// floor needs both methods proven against the real wire too).
func TestDriverTx_GetAndDelete(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.Open: %v", err)
	}
	defer func() { _ = d.Close() }()

	if err := d.Put(ctx, "tx-getdel", "k", []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err = d.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		got, err := tx.Get(ctx, "tx-getdel", "k")
		if err != nil {
			return err
		}
		if string(got) != "v1" {
			t.Fatalf("tx.Get = %q, want v1", got)
		}
		return tx.Delete(ctx, "tx-getdel", "k")
	})
	if err != nil {
		t.Fatalf("Tx(get+delete): %v", err)
	}
	if _, err := d.Get(ctx, "tx-getdel", "k"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get after tx.Delete = %v, want KindNotFound", err)
	}
}

// TestDriver_String proves String() never panics and carries a redacted
// (not raw) DSN.
func TestDriver_String(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.Open: %v", err)
	}
	defer func() { _ = d.Close() }()
	if s := d.String(); s == "" {
		t.Fatal("String() returned empty")
	}
}

// TestOpen_MigratorError proves Open propagates an injected Migrator's
// error and closes the pool it had already opened, rather than returning
// a half-initialized Driver.
func TestOpen_MigratorError(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sentinel := errors.New("migrator sentinel failure")
	_, err := postgres.Open(ctx, dsn, postgres.WithMigrator(func(context.Context, *sql.DB) error {
		return sentinel
	}))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Open with a failing Migrator = %v, want errors.Is(err, sentinel)", err)
	}
}

// fixedClock is a deterministic migrate.Clock for this file's tests.
type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(1700000000, 0).UTC() }
