// Purpose: MigrateV1's partial-failure/resume, missing-directory, nil-guard
// and factory-error tests — split from migrate_cmd_test.go (not named in
// this ticket's files_scope) under Art.10.3's 300-line-file cap, exactly
// as internal/migration/instruction_regen.go's own split into scan.go/
// diff.go/report.go/doc.go records for the identical reason. Shares
// fakeImporter/fakeFactory/newTestLedgerAt/yesOpts with migrate_cmd_test.go
// (same package).
// SPORT: internal.migration.MigrateV1/ADD (P1-E26-W10-S53-T4).
package migration

import (
	"context"
	"errors"
	"testing"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestMigratePartialFailureResumesOnlyFailedDomain proves: when one
// domain's importer fails, the domains that succeeded are StatusDone,
// the failed one is StatusError with the message, the overall call
// returns non-zero, and a re-run only re-invokes the failed domain.
func TestMigratePartialFailureResumesOnlyFailedDomain(t *testing.T) {
	dir := t.TempDir()
	ledger := newTestLedgerAt(t, dir)
	ff := newFakeFactory()
	for _, d := range DomainOrder {
		ff.importers[d] = &fakeImporter{result: migrationv1.DryRunResult{Domain: d}}
	}
	wantErr := cascade.New(cascade.KindIntegrity, "vault import refused")
	ff.importers[migrationv1.DomainVault].err = wantErr

	_, err := MigrateV1(context.Background(), ledger, ff.factory, yesOpts(dir))
	if err == nil {
		t.Fatal("a failed domain did not fail the overall run")
	}

	configRow, found, _ := ledger.ReadDomain(context.Background(), migrationv1.DomainConfig)
	if !found || configRow.Status != StatusDone {
		t.Fatalf("config (ran before the failure) is not StatusDone: %+v found=%v", configRow, found)
	}
	vaultRow, found, _ := ledger.ReadDomain(context.Background(), migrationv1.DomainVault)
	if !found || vaultRow.Status != StatusError || vaultRow.Error == "" {
		t.Fatalf("vault (the failed domain) is not StatusError with a message: %+v found=%v", vaultRow, found)
	}
	accountsRow, found, _ := ledger.ReadDomain(context.Background(), migrationv1.DomainAccounts)
	if !found || accountsRow.Status != StatusDone {
		t.Fatalf("accounts (ran after the failure) is not StatusDone: %+v found=%v", accountsRow, found)
	}

	// Resume: fix the vault importer and re-run. Only vault (StatusError)
	// and memory (never attempted, since accounts succeeded before
	// memory in DomainOrder) should be re-invoked; config and accounts
	// must not be.
	ff2 := newFakeFactory()
	for _, d := range DomainOrder {
		ff2.importers[d] = &fakeImporter{result: migrationv1.DryRunResult{Domain: d}}
	}
	if _, err := MigrateV1(context.Background(), ledger, ff2.factory, yesOpts(dir)); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	opened := map[migrationv1.Domain]bool{}
	for _, d := range ff2.opened {
		opened[d] = true
	}
	if opened[migrationv1.DomainConfig] || opened[migrationv1.DomainAccounts] {
		t.Fatalf("resume re-invoked an already-done domain: %v", ff2.opened)
	}
	if !opened[migrationv1.DomainVault] {
		t.Fatal("resume did not retry the failed domain")
	}
}

// TestMigrateMissingSourceRootRefusesWithoutLedgerWrite proves AC 7: a
// missing v1 directory refuses with a typed error and writes nothing to
// the ledger, distinct from a genuine per-domain import failure.
func TestMigrateMissingSourceRootRefusesWithoutLedgerWrite(t *testing.T) {
	dir := t.TempDir()
	ledger := newTestLedgerAt(t, dir)
	ff := newFakeFactory()

	_, err := MigrateV1(context.Background(), ledger, ff.factory, yesOpts(dir+"/does-not-exist"))
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("missing v1 dir did not refuse with KindNotFound: %v", err)
	}
	if len(ff.opened) != 0 {
		t.Fatalf("a missing v1 dir still opened importers: %v", ff.opened)
	}
	for _, d := range DomainOrder {
		if _, found, rerr := ledger.ReadDomain(context.Background(), d); rerr != nil || found {
			t.Fatalf("a missing v1 dir wrote a ledger row for %q: found=%v err=%v", d, found, rerr)
		}
	}
}

// TestMigrateV1RefusesWithNilFactoryOrLedger proves the constructor-style
// guards at the top of MigrateV1.
func TestMigrateV1RefusesWithNilFactoryOrLedger(t *testing.T) {
	dir := t.TempDir()
	ledger := newTestLedgerAt(t, dir)
	if _, err := MigrateV1(context.Background(), ledger, nil, yesOpts(dir)); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("nil factory = %v", err)
	}
	ff := newFakeFactory()
	if _, err := MigrateV1(context.Background(), nil, ff.factory, yesOpts(dir)); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("nil ledger = %v", err)
	}
}

// TestMigrateInteractiveConfirmDeclinedRefuses proves an interactive
// decline (Confirm returns false) refuses and writes nothing to the
// ledger or any destination. The refusal path DOES still compute a
// dry-run preview (so the caller can report what would have happened),
// using each importer's own zero-write DryRun path — it does not skip
// calling Import entirely, only skips ever writing the ledger or a real
// destination.
func TestMigrateInteractiveConfirmDeclinedRefuses(t *testing.T) {
	dir := t.TempDir()
	ledger := newTestLedgerAt(t, dir)
	ff := newFakeFactory()
	for _, d := range DomainOrder {
		ff.importers[d] = &fakeImporter{result: migrationv1.DryRunResult{Domain: d}}
	}
	_, err := MigrateV1(context.Background(), ledger, ff.factory, MigrateV1Options{
		SourceRoot: dir, Confirm: func() (bool, error) { return false, nil },
	})
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("a declined confirmation did not refuse with KindPermissionDenied: %v", err)
	}
	for _, d := range DomainOrder {
		if len(ff.importers[d].calls) > 0 && !ff.importers[d].calls[0].DryRun {
			t.Fatalf("domain %q was called with DryRun=false on a declined confirmation", d)
		}
		if _, found, rerr := ledger.ReadDomain(context.Background(), d); rerr != nil || found {
			t.Fatalf("a declined confirmation wrote a ledger row for %q: found=%v err=%v", d, found, rerr)
		}
	}
}

// TestMigrateFactoryErrorIsRecordedAsDomainFailure proves a factory-level
// error (the destination store itself could not open) is recorded as
// this one domain's StatusError, not silently dropped.
func TestMigrateFactoryErrorIsRecordedAsDomainFailure(t *testing.T) {
	dir := t.TempDir()
	ledger := newTestLedgerAt(t, dir)
	ff := newFakeFactory()
	for _, d := range DomainOrder {
		ff.importers[d] = &fakeImporter{result: migrationv1.DryRunResult{Domain: d}}
	}
	ff.factoryErr[migrationv1.DomainAccounts] = errors.New("destination store unavailable")

	_, err := MigrateV1(context.Background(), ledger, ff.factory, yesOpts(dir))
	if err == nil {
		t.Fatal("a factory error did not fail the overall run")
	}
	row, found, _ := ledger.ReadDomain(context.Background(), migrationv1.DomainAccounts)
	if !found || row.Status != StatusError {
		t.Fatalf("accounts factory error was not recorded: %+v found=%v", row, found)
	}
}
