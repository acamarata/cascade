package health

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: WithChooser's routing tests (P1-E40-W9-S78-T1), split out of
// health_test.go to stay under the 300-line cap. A fake Chooser records
// every call so these tests assert WHICH kind and WHICH id reached it,
// without depending on topology's real Chooser construction.

// fakeChooserRoute is a recording Chooser fake.
type fakeChooserRoute struct {
	calls []struct {
		dom  topology.DomainID
		cred topology.CredentialID
		kind cascade.Kind
	}
}

func (f *fakeChooserRoute) OnProviderError(_ context.Context, dom topology.DomainID, cred topology.CredentialID, err error) (topology.Retry, error) {
	kind, _ := cascade.KindOf(err)
	f.calls = append(f.calls, struct {
		dom  topology.DomainID
		cred topology.CredentialID
		kind cascade.Kind
	}{dom, cred, kind})
	return topology.Retry{}, nil
}

func fixedResolver(dom topology.DomainID, cred topology.CredentialID) ResolveCredential {
	return func(string) (topology.DomainID, topology.CredentialID, bool) { return dom, cred, true }
}

// TestDemoteProvider429RoutesDomainOnlyNeverSiblingProvider asserts a
// rate_limited_429 reaches the chooser as KindQuotaExhausted (the SCOPE/
// domain path) and never writes the registry -- so a sibling provider's
// own health_status is untouched by a 429 on a different one.
func TestDemoteProvider429RoutesDomainOnlyNeverSiblingProvider(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
	mgr, reg, _ := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	seedProvider(t, reg, "sibling")
	fc := &fakeChooserRoute{}
	mgr.WithChooser(fc, fixedResolver("d1", ""))
	ctx := context.Background()

	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
		t.Fatalf("DemoteProvider: %v", err)
	}
	if len(fc.calls) != 1 || fc.calls[0].kind != cascade.KindQuotaExhausted || fc.calls[0].dom != "d1" {
		t.Fatalf("unexpected chooser calls: %+v", fc.calls)
	}
	rec, err := reg.GetProvider(ctx, "p1")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if rec.HealthStatus != registry.HealthUnknown {
		t.Fatalf("routed 429 must not write the registry: health_status=%s", rec.HealthStatus)
	}
	sib, err := reg.GetProvider(ctx, "sibling")
	if err != nil {
		t.Fatalf("GetProvider(sibling): %v", err)
	}
	if sib.HealthStatus != registry.HealthUnknown || sib.DemotionCount != 0 {
		t.Fatalf("a 429 on p1 must never demote a sibling provider: %+v", sib)
	}
}

// TestDemoteProviderDeadKeyRoutesCredentialNeverDomain asserts
// dead_key_hard/dead_key_soft reach the chooser as KindPermissionDenied
// (the CREDENTIAL path), never KindCapabilityDenied (the 403/domain path).
func TestDemoteProviderDeadKeyRoutesCredentialNeverDomain(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
	mgr, reg, _ := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	fc := &fakeChooserRoute{}
	mgr.WithChooser(fc, fixedResolver("d1", "cred-1"))
	ctx := context.Background()

	for _, reason := range []provider.DemotionReason{provider.ReasonDeadKeyHard, provider.ReasonDeadKeySoft} {
		if err := mgr.DemoteProvider(ctx, "p1", reason); err != nil {
			t.Fatalf("DemoteProvider(%s): %v", reason, err)
		}
	}
	if len(fc.calls) != 2 {
		t.Fatalf("expected 2 chooser calls, got %d", len(fc.calls))
	}
	for _, c := range fc.calls {
		if c.kind != cascade.KindPermissionDenied || c.cred != "cred-1" {
			t.Fatalf("dead-key call did not route the credential: %+v", c)
		}
		if c.kind == cascade.KindCapabilityDenied {
			t.Fatal("a dead-key reason must never quarantine the domain")
		}
	}
}
