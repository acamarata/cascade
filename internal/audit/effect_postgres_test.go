//go:build postgres

package audit

// Purpose: run the effect tests against the P1-SEC-37 postgres store too.
// Inputs: CASCADE_TEST_POSTGRES_DSN, a real reachable PostgreSQL server.
// Outputs: a "postgres" entry in effectStores; the store skips loudly
//   without a DSN, never passes silently.
// Constraints: the audit namespace is emptied before each use, so every
//   subtest starts from a clean log on the shared database.
// SPORT: internal.audit.EffectLog/ADDED (tests) (P1-SEC-33).

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/postgres"
)

func init() {
	effectStores = append(effectStores, effectStore{"postgres", openPostgresEffectStore})
}

func openPostgresEffectStore(t *testing.T) provider.Store {
	t.Helper()
	dsn := os.Getenv("CASCADE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CASCADE_TEST_POSTGRES_DSN not set: the postgres effect lane needs a real PostgreSQL server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for key := range scanNamespace(t, d, "") {
		if err := d.Delete(ctx, namespace, key); err != nil {
			t.Fatalf("clearing %s: %v", key, err)
		}
	}
	return d
}
