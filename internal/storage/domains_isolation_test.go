// Purpose: domain-isolation test — proves the point of having separate
//
//	physical anchor tables per domain: a row written under one domain's
//	table is not visible through any other domain's table. Split from
//	domains_test.go as a sibling file per R-14.117 (Art.10.3 300-line
//	cap; mechanical relocation, no behavior change). Every .db file lives
//	in t.TempDir() (Art.7.1).
//
// SPORT: internal.storage.domains.AllDomains/ADDED,
//
//	internal.storage.domains.Bootstrap/ADDED (P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

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
