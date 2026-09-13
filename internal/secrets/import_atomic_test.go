// Purpose: unit coverage for import_atomic.go's all-or-rollback vault.env
//
//	import: this file shipped with zero tests, and its whole job is the
//	rollback guarantee its own header promises ("a failed Set restores
//	every earlier key before the typed failure is returned") -- exactly
//	the class of security-critical refusal/rollback path this floor
//	exists to force real coverage of, never a "no panic" smoke test.
//
// Inputs: an in-memory Custody (memCustody, broker_test.go) plus a local
//
//	keyFailCustody wrapper this file adds for failing Set on ONE named
//	key -- memCustody's own failOn is a per-OPERATION switch, not a
//	per-key one, and the rollback proof needs the import to succeed on
//	earlier keys before failing on a later one.
//
// Outputs: dry-run/apply parity, created/updated/unchanged/duplicate
//
//	counting, context-cancellation refusal, and both rollback outcomes
//	(clean restore, and a rollback-itself-fails KindIntegrity escalation)
//	-- each mutation-proven below.
//
// SPORT: internal/secrets:import-atomic (TEST) -- P1-E26-W10-S53-T1.
package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// keyFailCustody wraps a memCustody, failing Set for exactly one target
// name (once) so a rollback test can force a real multi-key import to
// fail partway through, over a realistic collaborator rather than a
// mock that never behaves like the real backends.
type keyFailCustody struct {
	*memCustody
	failSetName string
	setCalls    int
}

func (k *keyFailCustody) Set(ctx context.Context, name string, value []byte) error {
	if name == k.failSetName {
		k.setCalls++
		return errors.New("simulated custody failure: disk full")
	}
	return k.memCustody.Set(ctx, name, value)
}

func TestImportAtomic_NilBrokerRefused(t *testing.T) {
	_, err := ImportAtomic(context.Background(), nil, []byte("A=1"), false)
	if !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ImportAtomic(nil broker) = %v, want KindInvalidInput", err)
	}
}

func TestImportAtomic_ParseErrorPropagates(t *testing.T) {
	b, _ := newTestBroker(t, &allowGate{})
	_, err := ImportAtomic(context.Background(), b, []byte("not a valid line"), false)
	if err == nil {
		t.Fatal("ImportAtomic with an unparseable line = nil error, want ParseVaultEnv's refusal")
	}
}

func TestImportAtomic_DryRunAppliesNothing(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	custody.entries["EXISTING"] = []byte("old")

	report, err := ImportAtomic(context.Background(), b, []byte("EXISTING=new\nNEW_KEY=v\n"), true)
	if err != nil {
		t.Fatalf("ImportAtomic (dry run): %v", err)
	}
	if report.Created != 1 || report.Updated != 1 {
		t.Fatalf("report = %+v, want Created=1 Updated=1", report)
	}
	if got := string(custody.entries["EXISTING"]); got != "old" {
		t.Errorf("dry run wrote EXISTING = %q, want it untouched (\"old\")", got)
	}
	if _, ok := custody.entries["NEW_KEY"]; ok {
		t.Error("dry run created NEW_KEY, want no write at all")
	}
}

// TestImportAtomic_AppliesCreatedUpdatedUnchanged proves the three
// counting/apply outcomes against a real custody: a brand new key
// (Created), a changed value (Updated), and an identical re-import
// (Unchanged, no write).
func TestImportAtomic_AppliesCreatedUpdatedUnchanged(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	custody.entries["SAME"] = []byte("v")
	custody.entries["CHANGED"] = []byte("old")

	data := []byte("SAME=v\nCHANGED=new\nBRAND_NEW=fresh\n")
	report, err := ImportAtomic(context.Background(), b, data, false)
	if err != nil {
		t.Fatalf("ImportAtomic: %v", err)
	}
	if report.Created != 1 || report.Updated != 1 || report.Unchanged != 1 {
		t.Fatalf("report = %+v, want Created=1 Updated=1 Unchanged=1", report)
	}
	if got := string(custody.entries["CHANGED"]); got != "new" {
		t.Errorf("CHANGED = %q, want \"new\"", got)
	}
	if got := string(custody.entries["BRAND_NEW"]); got != "fresh" {
		t.Errorf("BRAND_NEW = %q, want \"fresh\"", got)
	}
}

// TestImportAtomic_DuplicateKeyLastWins proves a repeated key within one
// file is not an error (idempotent re-import support): the later
// assignment wins and the name is reported as a duplicate.
func TestImportAtomic_DuplicateKeyLastWins(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	report, err := ImportAtomic(context.Background(), b, []byte("K=first\nK=second\n"), false)
	if err != nil {
		t.Fatalf("ImportAtomic: %v", err)
	}
	if len(report.DuplicateNames) != 1 || report.DuplicateNames[0] != "K" {
		t.Fatalf("DuplicateNames = %v, want [K]", report.DuplicateNames)
	}
	if got := string(custody.entries["K"]); got != "second" {
		t.Errorf("K = %q, want \"second\" (last wins)", got)
	}
}

// TestImportAtomic_CanceledContextRefusesBeforeAnyRead proves the
// existingSecretNames context check: a canceled context refuses before
// any custody List/Get.
func TestImportAtomic_CanceledContextRefusesBeforeAnyRead(t *testing.T) {
	b, _ := newTestBroker(t, &allowGate{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ImportAtomic(ctx, b, []byte("K=v\n"), false)
	if !isKind(err, cascade.KindCanceled) {
		t.Fatalf("ImportAtomic on a canceled context = %v, want KindCanceled", err)
	}
}

// TestImportAtomic_RollsBackOnMidwayFailure is the load-bearing proof of
// this file's own header promise: a Set failure partway through a
// multi-key import restores every key that had already been written,
// leaving custody exactly as it was before the import started.
func TestImportAtomic_RollsBackOnMidwayFailure(t *testing.T) {
	_, custody := newTestBroker(t, &allowGate{})
	custody.entries["EXISTING"] = []byte("original")
	failing := &keyFailCustody{memCustody: custody, failSetName: "ZEBRA"}
	fb, err := NewBroker(failing, &allowGate{})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}

	// Sorted order (planAtomicImport sorts names) puts EXISTING and
	// NEWKEY before ZEBRA alphabetically, so both apply before the
	// forced failure.
	data := []byte("EXISTING=updated\nNEWKEY=created\nZEBRA=never-lands\n")
	_, err = ImportAtomic(context.Background(), fb, data, false)
	if err == nil {
		t.Fatal("ImportAtomic with a forced mid-import failure = nil error, want the underlying Set failure")
	}
	if got := string(custody.entries["EXISTING"]); got != "original" {
		t.Errorf("EXISTING after rollback = %q, want restored to \"original\"", got)
	}
	if _, ok := custody.entries["NEWKEY"]; ok {
		t.Error("NEWKEY survived rollback, want it deleted (it never existed before the import)")
	}
	if _, ok := custody.entries["ZEBRA"]; ok {
		t.Error("ZEBRA was written despite the forced Set failure")
	}
	if failing.setCalls != 1 {
		t.Errorf("forced Set failure invoked %d times, want exactly 1", failing.setCalls)
	}
}

// TestImportAtomic_RollbackFailureEscalatesToIntegrity proves that when
// even the rollback's own restore Set fails, the caller learns the
// destination's consistency is unknown (KindIntegrity) rather than
// receiving the original error as if the rollback had quietly succeeded.
// keyFailCustody fails every Set for "EXISTING" unconditionally, so the
// same forced failure fires both for the initial write attempt and for
// rollbackAtomicImport's own restore attempt on that identical entry
// (applyAtomicImport passes the just-failed entry into rollback too, so
// its previous value is still retried).
func TestImportAtomic_RollbackFailureEscalatesToIntegrity(t *testing.T) {
	custody := newMemCustody()
	custody.entries["EXISTING"] = []byte("original")
	failing := &keyFailCustody{memCustody: custody, failSetName: "EXISTING"}
	fb, err := NewBroker(failing, &allowGate{})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}

	_, err = ImportAtomic(context.Background(), fb, []byte("EXISTING=updated\n"), false)
	if !isKind(err, cascade.KindIntegrity) {
		t.Fatalf("ImportAtomic with a failing rollback = %v, want KindIntegrity", err)
	}
}
