// Purpose: idempotency + domain-layout + domain-isolation tests for
//
//	Bootstrap (§5.9, Art.2, Art.1). Every .db file lives in t.TempDir()
//	(Art.7.1); every assertion about table presence queries
//	sqlite_master on a real modernc-sqlite database directly — never a
//	self-authored schema double (Art.2). See testdata/README.md for
//	provenance.
//
// SPORT: internal.storage.domains.AllDomains/ADDED,
//
//	internal.storage.domains.Bootstrap/ADDED (P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// openTestDB opens a real modernc-sqlite database at a fresh t.TempDir()
// path, closing it automatically at test cleanup.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// sqliteMasterTableNames returns every table name sqlite_master reports —
// the Art.2 ground truth every test in this file checks Bootstrap's
// claims against.
func sqliteMasterTableNames(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	return names
}

// TestDomainLayout_AllDomainsDeterministic asserts AllDomains carries
// exactly the R-14.5 ten domains, in a fixed, repeatable order, and that
// every switch consumer of DomainID (domainLayoutExhaustiveSwitch below)
// compiles exhaustively — golangci-lint's `exhaustive` analyzer (R-14.101)
// is the enforcement layer that catches a forgotten case at lint time; this
// test proves the same ten values at run time.
func TestDomainLayout_AllDomainsDeterministic(t *testing.T) {
	want := []storage.DomainID{
		storage.DomainContext, storage.DomainMemory, storage.DomainAudit,
		storage.DomainSecrets, storage.DomainSessions, storage.DomainConfig,
		storage.DomainRetrieval, storage.DomainBlobs, storage.DomainQueue,
		storage.DomainJobs,
	}
	if len(storage.AllDomains) != len(want) {
		t.Fatalf("AllDomains has %d entries, want %d", len(storage.AllDomains), len(want))
	}
	for i, meta := range storage.AllDomains {
		if meta.ID != want[i] {
			t.Errorf("AllDomains[%d].ID = %q, want %q (order must be deterministic)", i, meta.ID, want[i])
		}
		if meta.TablePrefix == "" {
			t.Errorf("AllDomains[%d] (%s): empty TablePrefix", i, meta.ID)
		}
		if meta.OwnerPkg == "" {
			t.Errorf("AllDomains[%d] (%s): empty OwnerPkg", i, meta.ID)
		}
		if !domainLayoutExhaustiveSwitch(meta.ID) {
			t.Errorf("AllDomains[%d].ID = %q not recognized by the exhaustive switch (closed set violated)", i, meta.ID)
		}
	}

	// Repeated calls (Go const/var initialization is deterministic, but
	// this pins the invariant as a test rather than an assumption) return
	// the identical order.
	for i, meta := range storage.AllDomains {
		if meta.ID != want[i] {
			t.Fatalf("AllDomains order changed on re-read at index %d", i)
		}
	}
}

// domainLayoutExhaustiveSwitch mirrors the closed-set switch every real
// consumer (Bootstrap, StorageHealthCheck) must write: golangci-lint's
// `exhaustive` analyzer fails the build if a case is missing here, which
// is the point — a forgotten R-14.5 domain becomes a lint failure, not a
// silent gap. Returns true for every one of the ten recognized IDs.
func domainLayoutExhaustiveSwitch(id storage.DomainID) bool {
	switch id {
	case storage.DomainContext, storage.DomainMemory, storage.DomainAudit,
		storage.DomainSecrets, storage.DomainSessions, storage.DomainConfig,
		storage.DomainRetrieval, storage.DomainBlobs, storage.DomainQueue,
		storage.DomainJobs:
		return true
	}
	return false
}

// TestBootstrap_CreatesDomainAnchorTables proves Bootstrap's first call
// against a fresh database creates every AllDomains anchor table plus the
// reserved health-probe table, verified directly against sqlite_master —
// never a self-authored schema double (Art.2).
func TestBootstrap_CreatesDomainAnchorTables(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))

	report, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// Ten domain anchors + one reserved health-probe table.
	if report.TablesCreated != 11 {
		t.Errorf("TablesCreated = %d, want 11", report.TablesCreated)
	}

	tables := sqliteMasterTableNames(t, db)
	for _, meta := range storage.AllDomains {
		want := meta.TablePrefix + "_domain_root"
		if !tables[want] {
			t.Errorf("sqlite_master missing anchor table %q for domain %s", want, meta.ID)
		}
	}
	if !tables["applied_migrations"] {
		t.Error("sqlite_master missing applied_migrations ledger table")
	}
	if !tables["__health_probe__"] {
		t.Error("sqlite_master missing __health_probe__ reserved table")
	}

	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode;`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(schema_version) FROM applied_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != 1 {
		t.Errorf("stamped schema_version = %d, want 1", version)
	}
}

// TestBootstrapIdempotent proves the §5.9 idempotency contract: a second
// Bootstrap call on the same database returns TablesCreated: 0, and every
// domain-anchor table's row count is unchanged (proving no data mutation,
// not merely "no error").
func TestBootstrapIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	opts := storage.BootstrapOpts{Clock: clock}

	if _, err := storage.Bootstrap(ctx, db, opts); err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}

	// Write a marker row into one domain's anchor table so the second
	// Bootstrap call has real data to prove it never touches.
	if _, err := db.ExecContext(ctx, `INSERT INTO context_domain_root (id) VALUES (42)`); err != nil {
		t.Fatalf("seed marker row: %v", err)
	}
	before := rowCounts(t, db)

	report, err := storage.Bootstrap(ctx, db, opts)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	if report.TablesCreated != 0 {
		t.Errorf("second Bootstrap TablesCreated = %d, want 0", report.TablesCreated)
	}
	if report.Delta == "" {
		t.Error("second Bootstrap Delta is empty, want a zero-delta report")
	}

	after := rowCounts(t, db)
	for table, want := range before {
		if after[table] != want {
			t.Errorf("row count for %s changed: before=%d after=%d (Bootstrap must not mutate data on re-run)", table, want, after[table])
		}
	}

	var stampRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM applied_migrations WHERE schema_version = 1`).Scan(&stampRows); err != nil {
		t.Fatalf("count stamp rows: %v", err)
	}
	if stampRows != 1 {
		t.Errorf("applied_migrations has %d rows at schema_version=1 after two Bootstrap calls, want exactly 1 (no duplicate stamp)", stampRows)
	}
}

// rowCounts returns every domain anchor table's row count, keyed by table
// name, plus the health-probe and ledger tables.
func rowCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	tables := []string{"applied_migrations", "__health_probe__"}
	for _, meta := range storage.AllDomains {
		tables = append(tables, meta.TablePrefix+"_domain_root")
	}
	counts := map[string]int{}
	for _, table := range tables {
		var n int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM "`+table+`"`).Scan(&n); err != nil {
			t.Fatalf("count rows in %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// TestBootstrap_RequiresClock proves Bootstrap refuses a nil Clock rather
// than silently reading the wall clock (which forbidigo would in any case
// forbid at any call site in this package).
func TestBootstrap_RequiresClock(t *testing.T) {
	db := openTestDB(t)
	_, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{})
	if err == nil {
		t.Fatal("Bootstrap with nil Clock: want error, got nil")
	}
}

// TestBootstrap_FailsOnClosedDB proves Bootstrap propagates a real
// database error (rather than panicking) when db is already closed —
// PRAGMA journal_mode=WAL, the first statement Bootstrap issues, fails
// immediately.
func TestBootstrap_FailsOnClosedDB(t *testing.T) {
	db := openTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))

	_, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: clock})
	if err == nil {
		t.Fatal("Bootstrap on a closed database: want error, got nil")
	}
}

// TestBootstrap_FailsWhenLedgerTableCreateErrors drives Bootstrap's
// stampSchemaVersion/ensureLedgerTable CREATE-error branch: after a first
// successful Bootstrap, applied_migrations is dropped (so it is genuinely
// absent, not a no-op re-create) and the connection is switched to
// `PRAGMA query_only = 1`, under which SQLite refuses to create a table
// that does not yet exist with SQLITE_READONLY — verified empirically
// against modernc-sqlite (a CREATE TABLE IF NOT EXISTS on an ALREADY-
// present table is a true no-op under query_only and does not error,
// which is why this test drops the table first rather than reusing an
// existing one). The ten domain anchors and the health-probe table are
// all already present, so bootstrapDomainTables itself is a no-op and
// Bootstrap fails specifically inside the ledger-stamp step.
func TestBootstrap_FailsWhenLedgerTableCreateErrors(t *testing.T) {
	db := openTestDB(t)
	// PRAGMA query_only is per-connection; database/sql's pool may
	// otherwise hand a later call a different pooled connection than the
	// one query_only was set on, silently defeating this test. Pinning
	// the pool to one connection makes every statement in this test —
	// including Bootstrap's own — share the same underlying connection.
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DROP TABLE applied_migrations`); err != nil {
		t.Fatalf("drop applied_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA query_only = 1;`); err != nil {
		t.Fatalf("set query_only: %v", err)
	}

	_, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock})
	if err == nil {
		t.Fatal("Bootstrap with a genuinely-absent ledger table under PRAGMA query_only=1: want error, got nil")
	}
}

// TestDomainIsolation_WritesUnreachableAcrossDomains proves the point of
// having separate physical anchor tables per domain: a row written under
// one domain's table is not visible through any other domain's table, or
// through a query naively scoped to the wrong table name. Also asserts
// none of AllDomains's real TablePrefix values collides with the R-14.100
// reserved `plugin.__host__.*` PluginStorage namespace — this ticket does
// not implement PluginStorage (that is O/S-32.T3/T4's surface, layered on
// pkg/provider.Store's namespace argument, never on these anchor tables),
// but the closed ten-domain set must never encroach on a namespace R-14.100
// already reserved elsewhere.
func TestDomainIsolation_WritesUnreachableAcrossDomains(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	// Write a distinctive rowid into context_domain_root only.
	if _, err := db.ExecContext(ctx, `INSERT INTO context_domain_root (id) VALUES (777)`); err != nil {
		t.Fatalf("insert into context_domain_root: %v", err)
	}

	for _, meta := range storage.AllDomains {
		if meta.ID == storage.DomainContext {
			continue
		}
		table := meta.TablePrefix + "_domain_root"
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+table+`" WHERE id = 777`).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("domain isolation violated: id=777 written to context_domain_root is visible in %s (domain %s)", table, meta.ID)
		}
	}

	// Confirm it really is present where it belongs (a passing loop
	// above is meaningless if the write itself silently failed).
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM context_domain_root WHERE id = 777`).Scan(&n); err != nil {
		t.Fatalf("query context_domain_root: %v", err)
	}
	if n != 1 {
		t.Fatalf("marker row not found in context_domain_root: got %d rows, want 1", n)
	}

	// R-14.100 reserved-namespace non-collision: no domain's TablePrefix
	// equals or is contained by the reserved plugin.__host__ PluginStorage
	// namespace string, and the reserved namespace itself is not among
	// AllDomains's real prefixes.
	for _, meta := range storage.AllDomains {
		if meta.TablePrefix == storage.ReservedPluginHostNamespace {
			t.Errorf("domain %s TablePrefix %q collides with the R-14.100 reserved namespace", meta.ID, meta.TablePrefix)
		}
		if strings.Contains(storage.ReservedPluginHostNamespace, meta.TablePrefix) {
			t.Errorf("domain %s TablePrefix %q is a substring of the R-14.100 reserved namespace %q", meta.ID, meta.TablePrefix, storage.ReservedPluginHostNamespace)
		}
	}
}

// fakeGrantRegistry is a minimal storage.GrantRegistry double: it records
// every Register call, and fails from failAt onward (0 = never fail) so
// tests can drive both Bootstrap's optional-integration success path and
// its abort-on-registry-error path.
type fakeGrantRegistry struct {
	registered []storage.DomainID
	failAt     int // fail on the (1-indexed) call number >= failAt; 0 = never
}

func (f *fakeGrantRegistry) Register(_ context.Context, domain storage.DomainID) error {
	f.registered = append(f.registered, domain)
	if f.failAt != 0 && len(f.registered) >= f.failAt {
		return errFakeGrantRegistry
	}
	return nil
}

var errFakeGrantRegistry = fmt.Errorf("fakeGrantRegistry: refused")

// TestBootstrap_GrantRegistry_SelfGrantPerDomain proves Bootstrap calls
// GrantRegistry.Register exactly once per AllDomains entry, in order, when
// a non-nil GrantRegistry is supplied — the "optional integration" path
// the ticket describes.
func TestBootstrap_GrantRegistry_SelfGrantPerDomain(t *testing.T) {
	db := openTestDB(t)
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	registry := &fakeGrantRegistry{}

	if _, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: clock, GrantRegistry: registry}); err != nil {
		t.Fatalf("Bootstrap with GrantRegistry: %v", err)
	}
	if len(registry.registered) != len(storage.AllDomains) {
		t.Fatalf("GrantRegistry.Register called %d times, want %d", len(registry.registered), len(storage.AllDomains))
	}
	for i, meta := range storage.AllDomains {
		if registry.registered[i] != meta.ID {
			t.Errorf("Register call %d = %q, want %q (self-grant order must match AllDomains)", i, registry.registered[i], meta.ID)
		}
	}
}

// TestBootstrap_GrantRegistry_ErrorAborts proves a GrantRegistry.Register
// failure aborts Bootstrap with a wrapped error rather than being
// swallowed.
func TestBootstrap_GrantRegistry_ErrorAborts(t *testing.T) {
	db := openTestDB(t)
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	registry := &fakeGrantRegistry{failAt: 2}

	_, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: clock, GrantRegistry: registry})
	if err == nil {
		t.Fatal("Bootstrap with a failing GrantRegistry: want error, got nil")
	}
}
