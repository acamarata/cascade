// Purpose: the S-42.T6 escrow-guard tests -- split out of recovery_test.go
// to stay under the repo's 300-line-per-file gate. AuditEscrowChecker's
// false/true/nil-reader/query-error paths, and CreateSnapshot's
// engine-level guard on both the scheduled-fire and manual-create shapes,
// including the documented nil-Escrow backward-compatible default.
// SPORT: internal.backup.escrow/ADD (tests) (P1-E19-W4-S42-T6).
package backup

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newTestAuditLog builds a real, empty audit.Log over an in-memory Store --
// Art.2 real collaborator, never a fake Query implementation, for the
// escrow-check tests below.
func newTestAuditLog(t *testing.T) *audit.Log {
	t.Helper()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	return audit.New(store, clock, nil)
}

func TestAuditEscrowChecker_FalseUntilRecorded(t *testing.T) {
	ctx := context.Background()
	log := newTestAuditLog(t)
	checker := AuditEscrowChecker{Reader: log}

	escrowed, err := checker.Escrowed(ctx)
	if err != nil {
		t.Fatalf("Escrowed (before export): %v", err)
	}
	if escrowed {
		t.Fatal("Escrowed reported true before any key export was recorded")
	}

	if err := RecordKeyEscrowed(ctx, log, "backup"); err != nil {
		t.Fatalf("RecordKeyEscrowed: %v", err)
	}
	escrowed, err = checker.Escrowed(ctx)
	if err != nil {
		t.Fatalf("Escrowed (after export): %v", err)
	}
	if !escrowed {
		t.Fatal("Escrowed reported false after RecordKeyEscrowed")
	}
}

func TestAuditEscrowChecker_NilReaderRefuses(t *testing.T) {
	if _, err := (AuditEscrowChecker{}).Escrowed(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("AuditEscrowChecker{}.Escrowed (nil Reader) = %v, want KindUnavailable", err)
	}
}

// errAuditReader is an AuditReader test double that always fails, proving
// Escrowed propagates a query failure rather than reporting false.
type errAuditReader struct{}

func (errAuditReader) Query(context.Context, audit.Filter) (audit.Page, error) {
	return audit.Page{}, cascade.New(cascade.KindUnavailable, "errAuditReader: Query failed")
}

func TestAuditEscrowChecker_QueryErrorPropagates(t *testing.T) {
	checker := AuditEscrowChecker{Reader: errAuditReader{}}
	if _, err := checker.Escrowed(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Escrowed (query error) = %v, want KindUnavailable", err)
	}
}

// TestCreateRefusesBeforeEscrow proves the engine-level guard on BOTH
// paths named by the ticket: the scheduled-fire shape (empty proof --
// already refuses on ErrElevationRequired before escrow is ever
// consulted) and the manual-create shape (a real proof, but no escrow
// recorded yet -- refuses with ErrBackupKeyNotEscrowed). It then proves
// the state transition: once escrowed, the identical deps succeed.
func TestCreateRefusesBeforeEscrow(t *testing.T) {
	ctx := context.Background()
	setSigningKeyEnv(t)
	_, recipient := newTestAgeKeypair(t)
	log := newTestAuditLog(t)
	checker := AuditEscrowChecker{Reader: log}
	deps := CreateSnapshotDeps{
		Target: newMemTarget(), AgeRecipient: recipient,
		Clock:   testkit.NewFrozenClock(time.Unix(0, 0)),
		Domains: map[string]Exporter{"d": fakeExporter{data: []byte("x")}},
		Escrow:  checker,
	}

	// Scheduled-fire shape: empty proof refuses before escrow is ever
	// consulted, exactly like every other empty-proof call in this
	// package (schedule.go's fireBackupTarget always passes "").
	if _, err := CreateSnapshot(ctx, "", deps, nil); err != ErrElevationRequired {
		t.Fatalf("CreateSnapshot(scheduled shape, no escrow) = %v, want ErrElevationRequired", err)
	}

	// Manual-create shape: a real proof, but the ceremony has not run.
	if _, err := CreateSnapshot(ctx, "proof-1", deps, nil); err != ErrBackupKeyNotEscrowed {
		t.Fatalf("CreateSnapshot(manual shape, no escrow) = %v, want ErrBackupKeyNotEscrowed", err)
	}

	if err := RecordKeyEscrowed(ctx, log, "backup"); err != nil {
		t.Fatalf("RecordKeyEscrowed: %v", err)
	}

	// State transition: the identical deps now succeed. "Subsequent
	// snapshots are unaffected once the flag is set" (the ticket's own
	// words) is exactly this same deps value taking a second path.
	if _, err := CreateSnapshot(ctx, "proof-2", deps, nil); err != nil {
		t.Fatalf("CreateSnapshot(after escrow): %v", err)
	}
}

// TestCreateSnapshot_NilEscrowSkipsCheck proves the documented
// backward-compatible default: a caller that never sets Escrow (every
// pre-ticket CreateSnapshotDeps literal) is unaffected by this ticket.
func TestCreateSnapshot_NilEscrowSkipsCheck(t *testing.T) {
	ctx := context.Background()
	setSigningKeyEnv(t)
	_, recipient := newTestAgeKeypair(t)
	deps := CreateSnapshotDeps{
		Target: newMemTarget(), AgeRecipient: recipient,
		Clock:   testkit.NewFrozenClock(time.Unix(0, 0)),
		Domains: map[string]Exporter{"d": fakeExporter{data: []byte("x")}},
	}
	if _, err := CreateSnapshot(ctx, "proof-1", deps, nil); err != nil {
		t.Fatalf("CreateSnapshot(nil Escrow) = %v, want nil (documented no-op default)", err)
	}
}
