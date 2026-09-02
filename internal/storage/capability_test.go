// Purpose: fail-closed escape tests (plan parenthetical artifact, §5.11)
//
//	for CapabilityRegistry (capability.go), plus the shared registryChecker
//	adapter and testClock helper capability_bypass_test.go and
//	capability_isolation_test.go both reuse. Split across three sibling
//	files from the start (R-14.117, Art.10.3's 300-line cap) rather than
//	one large file, the same pattern domains_*_test.go already uses in
//	this package.
//
// SPORT: internal.storage.capability.CapabilityRegistry/ADDED,
//
//	internal.storage.capability.Grant/ADDED,
//	internal.storage.capability.Op/ADDED (P1-E02-W1-S02-T5).
package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/providers/sqlite"
)

// registryChecker adapts a *storage.CapabilityRegistry to providers/
// sqlite's locally-declared GrantChecker seam (executor.go) — the same
// shape a real composition-root adapter would take, kept here as
// test-only wiring proof. capability.go's package doc is explicit that
// this adapter is NOT yet assembled by any real, shipped caller: the
// plugin host that would need it arrives in a later ticket (Art.1).
// Shared by capability_isolation_test.go.
type registryChecker struct {
	reg *storage.CapabilityRegistry
}

func (c registryChecker) Check(ctx context.Context, src, dst string, op sqlite.CapOp) error {
	var sop storage.Op
	if op&sqlite.CapOpRead != 0 {
		sop |= storage.OpRead
	}
	if op&sqlite.CapOpWrite != 0 {
		sop |= storage.OpWrite
	}
	return c.reg.Check(ctx, storage.DomainID(src), storage.DomainID(dst), sop)
}

var _ sqlite.GrantChecker = registryChecker{}

// testClock returns a fixed testkit.FrozenClock (Art.7.3: no bare
// time.Now), shared across every capability_*_test.go file.
func testClock() *testkit.FrozenClock {
	return testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
}

// --- Fail-closed escape tests (plan parenthetical artifact, §5.11) ---

// TestFailClosed_NoGrant is escape case (a): a nil/unregistered grant
// returns storage.ErrDomainForbidden, not nil, not a panic.
func TestFailClosed_NoGrant(t *testing.T) {
	reg := storage.NewCapabilityRegistry(testClock())
	err := reg.Check(context.Background(), storage.DomainContext, storage.DomainMemory, storage.OpRead)
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("Check with no grant registered = %v, want ErrDomainForbidden", err)
	}
}

// TestFailClosed_RevokedGrant is escape case (b): a grant that existed and
// was then revoked returns storage.ErrDomainForbidden on the next Check —
// identical to never having been granted, per the fail-closed contract.
func TestFailClosed_RevokedGrant(t *testing.T) {
	ctx := context.Background()
	reg := storage.NewCapabilityRegistry(testClock())
	if err := reg.Grant(ctx, storage.Grant{SrcDomain: storage.DomainContext, DstDomain: storage.DomainMemory, Ops: storage.OpRead | storage.OpWrite}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := reg.Check(ctx, storage.DomainContext, storage.DomainMemory, storage.OpRead); err != nil {
		t.Fatalf("Check after Grant = %v, want nil", err)
	}
	if err := reg.Revoke(ctx, storage.DomainContext, storage.DomainMemory); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	err := reg.Check(ctx, storage.DomainContext, storage.DomainMemory, storage.OpRead)
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("Check after Revoke = %v, want ErrDomainForbidden", err)
	}
}

// TestFailClosed_WideningRejected is escape case (c): a grant covering
// only OpRead cannot satisfy a request for OpRead|OpWrite —
// storage.ErrCapabilityWidening, not a partial success.
func TestFailClosed_WideningRejected(t *testing.T) {
	ctx := context.Background()
	reg := storage.NewCapabilityRegistry(testClock())
	if err := reg.Grant(ctx, storage.Grant{SrcDomain: storage.DomainAudit, DstDomain: storage.DomainSecrets, Ops: storage.OpRead}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	err := reg.Check(ctx, storage.DomainAudit, storage.DomainSecrets, storage.OpRead|storage.OpWrite)
	if !errors.Is(err, storage.ErrCapabilityWidening) {
		t.Fatalf("Check widening request = %v, want ErrCapabilityWidening", err)
	}
	// The grant's own Ops still works when requested exactly.
	if err := reg.Check(ctx, storage.DomainAudit, storage.DomainSecrets, storage.OpRead); err != nil {
		t.Fatalf("Check within granted Ops = %v, want nil", err)
	}
}

// TestFailClosed_ValidGrantSucceeds is escape case (d): a valid, unexpired
// grant covering the requested Ops returns nil.
func TestFailClosed_ValidGrantSucceeds(t *testing.T) {
	ctx := context.Background()
	reg := storage.NewCapabilityRegistry(testClock())
	if err := reg.Grant(ctx, storage.Grant{SrcDomain: storage.DomainQueue, DstDomain: storage.DomainJobs, Ops: storage.OpWrite}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := reg.Check(ctx, storage.DomainQueue, storage.DomainJobs, storage.OpWrite); err != nil {
		t.Fatalf("Check with valid grant = %v, want nil", err)
	}
}

// TestFailClosed_ExpiredGrant proves an Exp in the past — evaluated
// through the injected Clock, never a bare time.Now — is treated
// identically to a revoked grant.
func TestFailClosed_ExpiredGrant(t *testing.T) {
	ctx := context.Background()
	clock := testClock()
	reg := storage.NewCapabilityRegistry(clock)
	if err := reg.Grant(ctx, storage.Grant{
		SrcDomain: storage.DomainConfig, DstDomain: storage.DomainRetrieval,
		Ops: storage.OpRead, Exp: clock.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := reg.Check(ctx, storage.DomainConfig, storage.DomainRetrieval, storage.OpRead); err != nil {
		t.Fatalf("Check before expiry = %v, want nil", err)
	}
	clock.Advance(2 * time.Minute)
	err := reg.Check(ctx, storage.DomainConfig, storage.DomainRetrieval, storage.OpRead)
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("Check after expiry = %v, want ErrDomainForbidden", err)
	}
}
