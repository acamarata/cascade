// Purpose: tests asserting every AD/S-61.T1 sentinel is a distinct,
//
//	non-nil *cascade.Error recognizable via errors.Is/cascade.HasKind.
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) — P1-E30-W6-S61-T1.
package provider_test

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestAgentSentinelsNonNilAndDistinct(t *testing.T) {
	sentinels := []error{
		provider.ErrEntitlement,
		provider.ErrJobNotFound,
		provider.ErrPlatform,
		provider.ErrHarnessIncompatible,
		provider.ErrDataClassDowngrade,
		provider.ErrOutOfOrderEvent,
		provider.ErrPreSpawnSecretFound,
		provider.ErrCancelUnconfirmed,
	}
	for i, e := range sentinels {
		if e == nil {
			t.Fatalf("sentinel %d is nil", i)
		}
		for j, other := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(e, other) {
				t.Fatalf("sentinel %d (%v) is not distinct from sentinel %d (%v)", i, e, j, other)
			}
		}
	}
}

func TestAgentSentinelKinds(t *testing.T) {
	cases := []struct {
		err  error
		kind cascade.Kind
	}{
		{provider.ErrEntitlement, cascade.KindPolicyDenied},
		{provider.ErrJobNotFound, cascade.KindNotFound},
		{provider.ErrPlatform, cascade.KindUnsupported},
		{provider.ErrHarnessIncompatible, cascade.KindConflict},
		{provider.ErrDataClassDowngrade, cascade.KindInvalidInput},
		{provider.ErrOutOfOrderEvent, cascade.KindIntegrity},
		{provider.ErrPreSpawnSecretFound, cascade.KindCapabilityDenied},
		{provider.ErrCancelUnconfirmed, cascade.KindTimeout},
	}
	for _, c := range cases {
		if !cascade.HasKind(c.err, c.kind) {
			t.Errorf("%v: want Kind %s", c.err, c.kind)
		}
	}
}
