// Purpose: tests asserting every AD/S-61.T1 sentinel is a distinct,
//
//	non-nil *cascade.Error recognizable via errors.Is/cascade.HasKind.
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) — P1-E30-W6-S61-T1.
package provider_test

import (
	"errors"
	"strings"
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

// TestAgentDriverSentinels covers the three AD/S-61.T2 sentinels. Their
// Kinds overlap the older ones (errors.Is compares Kind only), so this
// asserts identity, Kind and message rather than Kind-distinctness.
func TestAgentDriverSentinels(t *testing.T) {
	cases := []struct {
		err  error
		kind cascade.Kind
		msg  string
	}{
		{provider.ErrTranscriptPersistenceForbidden, cascade.KindPermissionDenied, "raw transcript"},
		{provider.ErrInvalidToolPolicy, cascade.KindInvalidInput, "tool policy"},
		{provider.ErrEgressSubstitutionFailed, cascade.KindUnavailable, "substitution failed"},
	}
	for i, c := range cases {
		if c.err == nil || !cascade.HasKind(c.err, c.kind) {
			t.Fatalf("%v: want non-nil Kind %s", c.err, c.kind)
		}
		if !strings.Contains(c.err.Error(), c.msg) {
			t.Fatalf("%v: message lacks %q", c.err, c.msg)
		}
		for j, other := range cases {
			if i != j && c.err == other.err {
				t.Fatalf("sentinel %d is the same value as %d", i, j)
			}
		}
	}
}

// TestErrMessageUnsupportedKind pins R-14.327 item 3: Message's sentinel is
// KindUnsupported, and since ErrPlatform shares that Kind (errors.Is on a
// *cascade.Error compares Kind only) the two are told apart by identity.
func TestErrMessageUnsupportedKind(t *testing.T) {
	err := provider.ErrMessageUnsupported
	if err == nil || !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("ErrMessageUnsupported = %v, want non-nil KindUnsupported", err)
	}
	if !strings.Contains(err.Error(), "mid-run turn") {
		t.Fatalf("ErrMessageUnsupported message %q does not say what is unsupported", err)
	}
	if error(err) == error(provider.ErrPlatform) {
		t.Fatal("ErrMessageUnsupported is the same value as ErrPlatform")
	}
	if !errors.Is(provider.ErrPlatform, err) {
		t.Fatal("Kind-only errors.Is no longer matches ErrPlatform: the identity rule's premise changed")
	}
}
