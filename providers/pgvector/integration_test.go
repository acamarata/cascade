//go:build postgres && integration

// Purpose: the pgvector storetest-under-docker lane's real conformance
//
//	run — RunVectorStoreTests against a REAL pgvector-enabled Postgres
//	server, plus the "extension missing" refusal path proven against a
//	real (non-pgvector) Postgres server, never a hand-rolled fake
//	(Art.2).
//
// Inputs: CASCADE_TEST_POSTGRES_DSN — MUST point at a server with the
//
//	pgvector extension available (the pgvector/pgvector Docker image;
//	see providers/pgvector/testdata/README.md). CASCADE_TEST_POSTGRES_NO_VECTOR_DSN
//	is optional and, when set, must point at a plain postgres:17 server
//	with no pgvector extension installed, to prove ensureExtension's
//	refusal against a genuinely incapable server rather than simulating
//	one.
//
// Constraints: go:build postgres && integration, matching
//
//	providers/postgres/integration_test.go's tag pair exactly.
//
// SPORT: providers.pgvector.VectorStore/ADDED (P1-E17-W4-S38-T4).
package pgvector_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/pgvector"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CASCADE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CASCADE_TEST_POSTGRES_DSN not set — this lane requires a real pgvector-enabled Postgres server (see providers/pgvector/testdata/README.md)")
	}
	return dsn
}

// TestPgvectorStoretestVectorUnderDocker is the ticket's named CI entry
// point: the full provider.VectorStore conformance suite against a REAL
// pgvector-enabled server.
func TestPgvectorStoretestVectorUnderDocker(t *testing.T) {
	dsn := testDSN(t)
	storetest.RunVectorStoreTests(t, func(t *testing.T) provider.VectorStore {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		d, err := pgvector.Open(ctx, dsn)
		if err != nil {
			t.Fatalf("pgvector.Open: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	})
}

// TestPgvectorOpen_ExtensionMissing proves the "extension missing"
// fail-closed refusal against a REAL server that genuinely lacks
// pgvector — never simulated. Skipped when
// CASCADE_TEST_POSTGRES_NO_VECTOR_DSN is not provided, since most
// environments running this lane only stand up the pgvector-enabled
// image.
func TestPgvectorOpen_ExtensionMissing(t *testing.T) {
	dsn := os.Getenv("CASCADE_TEST_POSTGRES_NO_VECTOR_DSN")
	if dsn == "" {
		t.Skip("CASCADE_TEST_POSTGRES_NO_VECTOR_DSN not set — this case needs a real server without pgvector installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pgvector.Open(ctx, dsn)
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("Open(no-pgvector server) = %v, want KindUnsupported", err)
	}
}

// TestPgvectorOpen_UnreachableServer proves the "unreachable server" error
// path against a real (non-listening) address.
func TestPgvectorOpen_UnreachableServer(t *testing.T) {
	testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := pgvector.Open(ctx, "postgres://postgres:test@127.0.0.1:1/cascade_test?sslmode=disable&connect_timeout=1")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Open(unreachable) = %v, want KindUnavailable", err)
	}
}

// TestDriver_String proves String() never panics.
func TestDriver_String(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := pgvector.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("pgvector.Open: %v", err)
	}
	defer func() { _ = d.Close() }()
	if s := d.String(); s == "" {
		t.Fatal("String() returned empty")
	}
}

// TestDriver_ErrorPathsAfterClose exercises Upsert/Delete/Count/Namespaces'
// error-wrapping branches (wrapDBError) by driving them against a Driver
// whose pool is already closed — a real database/sql-level error, not a
// simulated one, that classifyPgError's default-fallthrough branch maps to
// KindUnavailable.
func TestDriver_ErrorPathsAfterClose(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := pgvector.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("pgvector.Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := d.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{1}}}); err == nil {
		t.Fatal("Upsert after Close = nil, want an error")
	}
	if err := d.Delete(ctx, "ns", []string{"a"}); err == nil {
		t.Fatal("Delete after Close = nil, want an error")
	}
	if _, err := d.Count(ctx, "ns"); err == nil {
		t.Fatal("Count after Close = nil, want an error")
	}
	if _, err := d.Namespaces(ctx); err == nil {
		t.Fatal("Namespaces after Close = nil, want an error")
	}
}

// TestQuery_DimensionMismatchIsInvalidInput proves that mixing embedding
// dimensionalities within one namespace surfaces as a typed
// cascade.KindInvalidInput (pgvector's own SQLSTATE 22000 "different
// vector dimensions" refusal), never a generic KindUnavailable — a
// caller-side mistake, not a backend outage.
func TestQuery_DimensionMismatchIsInvalidInput(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := pgvector.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("pgvector.Open: %v", err)
	}
	defer func() { _ = d.Close() }()

	const ns = "dim-mismatch-ns"
	if err := d.Upsert(ctx, ns, []provider.Vector{{ID: "a", Values: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert 3-dim: %v", err)
	}
	_, err = d.Query(ctx, ns, provider.VectorQuery{Values: []float32{1}, TopK: 1})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Query(dimension mismatch) = %v, want KindInvalidInput", err)
	}
}
