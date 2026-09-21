//go:build windows

// Purpose: proves, on the Windows lane itself (R-14.131: a platform-
// specific behavior needs a test that RUNS on that platform, not merely
// builds), that installElevator.Elevate's real-attestation ceremony
// refuses cleanly with elevation.ErrWindowsTier2 when
// platformElevationRefusal (internal/rpc/elevation_windows.go) preempts
// it -- even when the injected fake keystore reports a non-tier2 Tier()
// (elevation.TierOSKeychain, the shape newFakeElevationKeystore's default
// and every POSIX-only real-ceremony test in this package uses). Before
// the fix (issueInstallChallenge, cascadepa_install_elevator.go), this
// exact case mislabeled the platform's by-design nonce-less refusal
// (internal/rpc/elevation_flow_windows_test.go) as
// `cascade.KindIntegrity "cascade-pa install: elevation challenge has no
// nonce"` -- the CI failure this file's fix and the
// ...elevator_posix_test.go build-tag split together resolve. Mirrors
// cmd/cascade/backup_windows_tier2_test.go exactly (P1-E19-W4-S42-T3).
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4
//
//	(CI fix ci-fix12).
package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestInstallElevator_WindowsTier2EvenWithNonTier2Keystore(t *testing.T) {
	ks := newFakeElevationKeystore(t) // tier: elevation.TierOSKeychain, not TierWindowsTier2
	e, dir := newTestInstallElevator(t, ks, func(string) string { return "" })
	enroll(t, ks, dir)

	res, err := e.Elevate(context.Background(), testElevationRequest())
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("Elevate on Windows with an enrolled, non-tier2-reporting keystore = %v (kind ok=%v kind=%v), want the ErrWindowsTier2 refusal (KindUnsupported)", err, ok, kind)
	}
	if res.Approved {
		t.Fatal("Approved = true on Windows -- elevation must never succeed on a tier-2 platform")
	}
	// this repo's cascade sentinels compare Kind only (errors.Is on two
	// cascade.New errors of the same Kind is not identity), so also
	// assert the message never regresses to the pre-fix mislabel.
	if strings.Contains(err.Error(), "no nonce") {
		t.Fatalf("Elevate on Windows leaked the nonce-less-challenge integrity message instead of the tier-2 refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "tier-2") {
		t.Fatalf("Elevate on Windows: err = %v, want elevation.ErrWindowsTier2's tier-2 message", err)
	}
	if res.Witness.Valid() {
		t.Fatal("Witness.Valid() = true on a refused elevation, want the zero witness")
	}
}
