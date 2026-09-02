// Purpose: bypass-surface probes for CapabilityRegistry.Check/Grant —
//
//	unknown domain, empty domain, the R-14.100 reserved plugin.__host__
//	namespace, exact grant scoping, and the Register/Grant/Check sharing
//	proof. Split from capability_test.go as a sibling file per R-14.117
//	(Art.10.3 300-line cap). Shares registryChecker/testClock with
//	capability_test.go (same package storage_test).
//
// SPORT: internal.storage.capability.CapabilityRegistry/ADDED (P1-E02-W1-S02-T5).
package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// TestCapability_SameDomainNeedsNoGrant proves a domain never needs a
// capability grant to touch its own data — Check(d, d, op) always
// succeeds for a valid domain with an empty grant map.
func TestCapability_SameDomainNeedsNoGrant(t *testing.T) {
	reg := storage.NewCapabilityRegistry(testClock())
	if err := reg.Check(context.Background(), storage.DomainMemory, storage.DomainMemory, storage.OpRead|storage.OpWrite); err != nil {
		t.Fatalf("same-domain Check = %v, want nil", err)
	}
}

// TestCapability_UnknownDomainRejected: a domain string that is not a
// member of the closed R-14.5 set is fail-closed rejected, even as
// src==dst (a forged domain cannot self-grant its way in).
func TestCapability_UnknownDomainRejected(t *testing.T) {
	ctx := context.Background()
	reg := storage.NewCapabilityRegistry(testClock())
	bogus := storage.DomainID("not-a-real-domain")
	if err := reg.Check(ctx, bogus, storage.DomainMemory, storage.OpRead); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Check(bogus, memory) = %v, want ErrDomainForbidden", err)
	}
	if err := reg.Check(ctx, bogus, bogus, storage.OpRead); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Check(bogus, bogus) = %v, want ErrDomainForbidden (unknown domain self-check must not bypass validation)", err)
	}
}

// TestCapability_EmptyDomainRejected: the empty DomainID is fail-closed
// rejected on either side, including as a same-value "self" check.
func TestCapability_EmptyDomainRejected(t *testing.T) {
	ctx := context.Background()
	reg := storage.NewCapabilityRegistry(testClock())
	empty := storage.DomainID("")
	if err := reg.Check(ctx, empty, storage.DomainMemory, storage.OpRead); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Check(\"\", memory) = %v, want ErrDomainForbidden", err)
	}
	if err := reg.Check(ctx, empty, empty, storage.OpRead); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Check(\"\", \"\") = %v, want ErrDomainForbidden", err)
	}
}

// TestCapability_ReservedPluginNamespaceRejected: R-14.100's reserved
// "plugin.__host__" PluginStorage namespace is not a domain and must
// never be usable as one — a plugin cannot claim it as a self-granting
// domain to reach real domain data.
func TestCapability_ReservedPluginNamespaceRejected(t *testing.T) {
	ctx := context.Background()
	reg := storage.NewCapabilityRegistry(testClock())
	reserved := storage.DomainID(storage.ReservedPluginHostNamespace)
	if err := reg.Check(ctx, reserved, reserved, storage.OpRead|storage.OpWrite); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Check(reserved, reserved) = %v, want ErrDomainForbidden", err)
	}
	if err := reg.Check(ctx, reserved, storage.DomainSecrets, storage.OpRead); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Check(reserved, secrets) = %v, want ErrDomainForbidden", err)
	}
}

// TestCapability_GrantScopedExactly proves a grant for (A, B) confers
// nothing for (A, C) or (C, B) — cross-domain access is scoped to exactly
// what was granted, never silently widened to a third domain.
func TestCapability_GrantScopedExactly(t *testing.T) {
	ctx := context.Background()
	reg := storage.NewCapabilityRegistry(testClock())
	if err := reg.Grant(ctx, storage.Grant{SrcDomain: storage.DomainContext, DstDomain: storage.DomainMemory, Ops: storage.OpRead | storage.OpWrite}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := reg.Check(ctx, storage.DomainContext, storage.DomainAudit, storage.OpRead); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Check(context, audit) after granting (context, memory) = %v, want ErrDomainForbidden", err)
	}
	if err := reg.Check(ctx, storage.DomainAudit, storage.DomainMemory, storage.OpRead); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Check(audit, memory) after granting (context, memory) = %v, want ErrDomainForbidden", err)
	}
}

// TestCapability_RegisterIsGrantRegistry proves CapabilityRegistry.Register
// (domains.go's pre-existing GrantRegistry seam) and Grant/Check share one
// registry: a domain's self-grant registration does not, by itself, confer
// any cross-domain access.
func TestCapability_RegisterIsGrantRegistry(t *testing.T) {
	ctx := context.Background()
	reg := storage.NewCapabilityRegistry(testClock())
	if err := reg.Register(ctx, storage.DomainBlobs); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Check(ctx, storage.DomainBlobs, storage.DomainBlobs, storage.OpRead|storage.OpWrite); err != nil {
		t.Fatalf("Check(blobs, blobs) after Register = %v, want nil", err)
	}
	if err := reg.Check(ctx, storage.DomainBlobs, storage.DomainQueue, storage.OpRead); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Check(blobs, queue) after Register(blobs) = %v, want ErrDomainForbidden (self-grant must not become cross-domain)", err)
	}
}

// TestCapability_GrantRejectsUnknownDomain proves Grant itself validates
// domain membership rather than silently storing a grant that could never
// legitimately match a later Check call.
func TestCapability_GrantRejectsUnknownDomain(t *testing.T) {
	reg := storage.NewCapabilityRegistry(testClock())
	err := reg.Grant(context.Background(), storage.Grant{SrcDomain: storage.DomainID("bogus"), DstDomain: storage.DomainMemory, Ops: storage.OpRead})
	if err == nil {
		t.Fatal("Grant with an unknown SrcDomain: want error, got nil")
	}
}
