//go:build windows

// Purpose: the Windows tier-2 assertion (Article 5, this ticket's AC 9):
// MigrateV1 has no daemon-dispatch branch at all — every platform,
// Windows included, calls the injected ImporterFactory in-process. This
// file exists so the Windows CI lane exercises that claim for real rather
// than only compiling it, matching internal/migration's own
// instruction_regen_windows_test.go precedent for a windows-tagged
// behavioral assertion in this package.
// SPORT: internal.migration.MigrateV1/ADD (P1-E26-W10-S53-T4).
package migration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestMigrateV1_WindowsEmbeddedOnly proves MigrateV1 runs every domain's
// importer in-process on Windows: the factory this test injects is a
// plain Go function with no socket, no daemon client, and no platform
// branch, and MigrateV1 calls it directly for every domain — there is no
// separate "daemon dispatch" code path to skip.
func TestMigrateV1_WindowsEmbeddedOnly(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(context.Background(), filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ledger, err := NewLedgerStore(db, testkit.NewFrozenClock(time.Now()))
	if err != nil {
		t.Fatalf("NewLedgerStore: %v", err)
	}

	opened := map[migrationv1.Domain]bool{}
	factory := func(_ context.Context, domain migrationv1.Domain) (migrationv1.Importer, func() error, error) {
		opened[domain] = true
		return inProcessImporter{result: migrationv1.DryRunResult{Domain: domain}}, func() error { return nil }, nil
	}

	report, err := MigrateV1(context.Background(), ledger, factory,
		MigrateV1Options{SourceRoot: dir, Yes: true})
	if err != nil {
		t.Fatalf("MigrateV1 on windows: %v", err)
	}
	for _, d := range DomainOrder {
		if !opened[d] {
			t.Fatalf("domain %q was never opened by the in-process factory", d)
		}
	}
	if len(report.Domains) != len(DomainOrder) {
		t.Fatalf("report has %d domains, want %d", len(report.Domains), len(DomainOrder))
	}
}

// inProcessImporter is a minimal, real (non-nil, real method call)
// Importer implementation used only to prove the in-process call path —
// it holds no socket and no platform-specific code.
type inProcessImporter struct{ result migrationv1.DryRunResult }

func (i inProcessImporter) Import(_ context.Context, _ migrationv1.Request) (migrationv1.DryRunResult, error) {
	return i.result, nil
}
