//go:build windows

// Purpose: Art.5 per-platform matrix test — proves every ElevationKeystore
//
//	method refuses with ErrWindowsTier2 on Windows, with the documented
//	reason string, never a silent success. This file only builds/runs on
//	a Windows GOOS target; it was NOT executed in this ticket's darwin
//	development sandbox (see the ticket journal's platform-coverage
//	section) — it is proven only by `GOOS=windows CGO_ENABLED=0 go
//	build ./...` compiling cleanly plus CI's own windows runner.
//
// SPORT: internal/elevation windows matrix test/ADDED (P1-E04-W1-S07-T6).
package elevation

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWindowsTier2_AllMethodsRefuse(t *testing.T) {
	ks := NewKeystore()

	if ks.IsAvailable() {
		t.Error("IsAvailable must be false on Windows")
	}
	if ks.Tier() != TierWindowsTier2 {
		t.Errorf("Tier() = %v, want TierWindowsTier2", ks.Tier())
	}

	assertTier2 := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected ErrWindowsTier2, got nil (silent success is forbidden on Windows)")
		}
		kind, ok := cascade.KindOf(err)
		if !ok || kind != cascade.KindUnsupported {
			t.Errorf("kind = %v (ok=%v), want KindUnsupported", kind, ok)
		}
		if !strings.Contains(err.Error(), "tier-2") {
			t.Errorf("error %q does not carry the documented tier-2 reason string", err.Error())
		}
	}

	assertTier2(t, ks.GenerateKey())
	_, err := ks.PubKeyB64()
	assertTier2(t, err)
	_, err = ks.Sign([]byte("payload"))
	assertTier2(t, err)
}
