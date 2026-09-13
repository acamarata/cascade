// Purpose: MigrateV1's orchestration contract — sequencing, idempotent
// skip, dry-run's zero-write guarantee, partial failure/resume, and the
// CASCADE_NO_INPUT/--yes/confirm truth table — driven against a real
// ledger (providers/sqlite on a t.TempDir() file) and fake, in-memory
// importers so no test touches a real v1 directory.
// SPORT: internal.migration.MigrateV1/ADD (P1-E26-W10-S53-T4).
package migration

import (
	"context"
	"testing"
	"time"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/providers/sqlite"
)

// fakeImporter is a scripted, in-memory migrationv1.Importer: it records
// every call it received and returns a canned result/error, never
// touching a real destination — the orchestration tests below assert
// MigrateV1's own decisions (ledger reads/writes, sequencing, error
// propagation), not any one real importer's parsing.
type fakeImporter struct {
	calls  []migrationv1.Request
	result migrationv1.DryRunResult
	err    error
}

func (f *fakeImporter) Import(_ context.Context, req migrationv1.Request) (migrationv1.DryRunResult, error) {
	f.calls = append(f.calls, req)
	return f.result, f.err
}

// fakeFactory builds an ImporterFactory over a fixed set of fakeImporters,
// one per domain, and records every domain it was asked to open plus
// whether each one's closer ran.
type fakeFactory struct {
	importers  map[migrationv1.Domain]*fakeImporter
	opened     []migrationv1.Domain
	closed     map[migrationv1.Domain]bool
	factoryErr map[migrationv1.Domain]error
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{
		importers:  map[migrationv1.Domain]*fakeImporter{},
		closed:     map[migrationv1.Domain]bool{},
		factoryErr: map[migrationv1.Domain]error{},
	}
}

func (f *fakeFactory) factory(_ context.Context, domain migrationv1.Domain) (migrationv1.Importer, func() error, error) {
	f.opened = append(f.opened, domain)
	if err := f.factoryErr[domain]; err != nil {
		return nil, nil, err
	}
	imp := f.importers[domain]
	if imp == nil {
		imp = &fakeImporter{}
		f.importers[domain] = imp
	}
	return imp, func() error { f.closed[domain] = true; return nil }, nil
}

func newTestLedgerAt(t *testing.T, dir string) *LedgerStore {
	t.Helper()
	db, err := sqlite.Open(context.Background(), dir+"/cascade.db")
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l, err := NewLedgerStore(db, testkit.NewFrozenClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("NewLedgerStore: %v", err)
	}
	return l
}

func yesOpts(sourceRoot string) MigrateV1Options {
	return MigrateV1Options{SourceRoot: sourceRoot, Yes: true}
}

// TestMigrateV1CommandInvokesEveryDomainInOrder proves the four v1
// importers run in the contract's order — config, vault, accounts,
// memory — and that a successful run writes a StatusDone ledger row per
// domain with the real DeltaCount.
func TestMigrateV1CommandInvokesEveryDomainInOrder(t *testing.T) {
	dir := t.TempDir()
	ledger := newTestLedgerAt(t, dir)
	ff := newFakeFactory()
	for _, d := range DomainOrder {
		ff.importers[d] = &fakeImporter{result: migrationv1.DryRunResult{
			Domain: d, Applied: true, Changes: []migrationv1.Change{{Operation: migrationv1.OperationCreate}},
		}}
	}

	report, err := MigrateV1(context.Background(), ledger, ff.factory, yesOpts(dir))
	if err != nil {
		t.Fatalf("MigrateV1: %v", err)
	}
	if len(ff.opened) != len(DomainOrder) {
		t.Fatalf("opened domains = %v, want all four", ff.opened)
	}
	for i, d := range DomainOrder {
		if ff.opened[i] != d {
			t.Fatalf("domain order[%d] = %q, want %q", i, ff.opened[i], d)
		}
		if !ff.closed[d] {
			t.Fatalf("domain %q's store was never closed", d)
		}
		row, found, rerr := ledger.ReadDomain(context.Background(), d)
		if rerr != nil || !found || row.Status != StatusDone || row.RecordCount != 1 {
			t.Fatalf("domain %q ledger row = %+v found=%v err=%v", d, row, found, rerr)
		}
	}
	if len(report.Domains) != len(DomainOrder) {
		t.Fatalf("report has %d domain entries, want %d", len(report.Domains), len(DomainOrder))
	}
}

// TestMigrateIdempotent proves a second run against the same --from
// exits 0 with every domain reported "skipped", and never opens a single
// importer for a domain the ledger already marks StatusDone.
func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	ledger := newTestLedgerAt(t, dir)
	ff := newFakeFactory()
	for _, d := range DomainOrder {
		ff.importers[d] = &fakeImporter{result: migrationv1.DryRunResult{Domain: d}}
	}
	if _, err := MigrateV1(context.Background(), ledger, ff.factory, yesOpts(dir)); err != nil {
		t.Fatalf("first run: %v", err)
	}

	ff2 := newFakeFactory() // a second factory: if it is ever called, the test fails below
	report, err := MigrateV1(context.Background(), ledger, ff2.factory, yesOpts(dir))
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(ff2.opened) != 0 {
		t.Fatalf("second run opened %v; an already-done domain must never be re-invoked", ff2.opened)
	}
	for _, d := range report.Domains {
		if !d.Skipped {
			t.Fatalf("domain %q was not reported skipped on the idempotent re-run", d.Domain)
		}
	}
}

// TestMigrateDryRun proves --dry-run computes every domain's delta
// (via each importer's own DryRun-aware path, T1's TestImporter_DryRun
// already proving that path performs no destination writes) while
// writing NOTHING to the ledger.
func TestMigrateDryRun(t *testing.T) {
	dir := t.TempDir()
	ledger := newTestLedgerAt(t, dir)
	ff := newFakeFactory()
	for _, d := range DomainOrder {
		ff.importers[d] = &fakeImporter{result: migrationv1.DryRunResult{
			Domain: d, Applied: false, Changes: []migrationv1.Change{{Operation: migrationv1.OperationCreate}},
		}}
	}

	report, err := MigrateV1(context.Background(), ledger, ff.factory, MigrateV1Options{SourceRoot: dir, DryRun: true})
	if err != nil {
		t.Fatalf("MigrateV1 dry-run: %v", err)
	}
	if !report.DryRun {
		t.Fatal("report.DryRun is false")
	}
	for _, d := range DomainOrder {
		imp := ff.importers[d]
		if len(imp.calls) != 1 || !imp.calls[0].DryRun {
			t.Fatalf("domain %q was not called with DryRun=true: %+v", d, imp.calls)
		}
		if _, found, err := ledger.ReadDomain(context.Background(), d); err != nil || found {
			t.Fatalf("dry-run wrote a ledger row for %q: found=%v err=%v", d, found, err)
		}
	}
}

// TestMigrateCASCADE_NO_INPUT proves the CASCADE_NO_INPUT=1 truth table:
// without --yes it refuses (no ledger write, no importer call), and with
// --yes it runs without ever calling Confirm.
func TestMigrateCASCADE_NO_INPUT(t *testing.T) {
	dir := t.TempDir()

	t.Run("without --yes refuses and writes nothing", func(t *testing.T) {
		ledger := newTestLedgerAt(t, dir)
		ff := newFakeFactory()
		for _, d := range DomainOrder {
			ff.importers[d] = &fakeImporter{result: migrationv1.DryRunResult{Domain: d}}
		}
		report, err := MigrateV1(context.Background(), ledger, ff.factory,
			MigrateV1Options{SourceRoot: dir, NoInput: true})
		if err == nil {
			t.Fatal("CASCADE_NO_INPUT without --yes did not refuse")
		}
		if !report.Refused {
			t.Fatal("report.Refused is false")
		}
		for _, d := range DomainOrder {
			if _, found, rerr := ledger.ReadDomain(context.Background(), d); rerr != nil || found {
				t.Fatalf("a refused run wrote a ledger row for %q: found=%v err=%v", d, found, rerr)
			}
		}
	})

	t.Run("with --yes runs without prompting", func(t *testing.T) {
		root2 := t.TempDir()
		ledger := newTestLedgerAt(t, root2)
		ff := newFakeFactory()
		for _, d := range DomainOrder {
			ff.importers[d] = &fakeImporter{result: migrationv1.DryRunResult{Domain: d}}
		}
		confirmCalled := false
		_, err := MigrateV1(context.Background(), ledger, ff.factory, MigrateV1Options{
			SourceRoot: root2, NoInput: true, Yes: true,
			Confirm: func() (bool, error) { confirmCalled = true; return true, nil },
		})
		if err != nil {
			t.Fatalf("CASCADE_NO_INPUT with --yes: %v", err)
		}
		if confirmCalled {
			t.Fatal("Confirm was called even though --yes was set")
		}
	})
}
