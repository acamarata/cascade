//go:build !windows

// Purpose: the one test that needs the full real elevation ceremony to
// actually complete with Approved:true -- a genuine nonce-issue,
// local-auth-sign, and attestation-verify round trip all succeeding. That
// completion is architecturally unreachable on Windows:
// platformElevationRefusal (internal/rpc/elevation_windows.go) preempts
// issueInstallChallenge (cascadepa_install_elevator.go) with a
// nonce-less ELEVATION_REQUIRED refusal before the handler ever runs
// (internal/rpc/elevation_flow_windows_test.go), so this test carries the
// same `!windows` tag as cmd/cascade/backup_elevation_realceremony_test.go's
// analogous POSIX-only proof (P1-E19-W4-S42-T3) and this file's own
// header mirrors that precedent exactly. Its Windows-side counterpart --
// the same ceremony provably refusing cleanly, per R-14.131 -- is
// cascadepa_install_elevator_windows_test.go. Fixtures live in the
// untagged cascadepa_install_elevator_helpers_test.go.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4
//
//	(CI fix ci-fix12).
package plugins

import (
	"context"
	"testing"
)

// TestInstallElevator_EnrolledAndSignedApproves is the success-path proof:
// an enrolled, correctly-signing keystore's real nonce challenge, local
// authentication, and rpc.VerifyAttestation round trip all succeed,
// minting Approved:true with a valid witness.
func TestInstallElevator_EnrolledAndSignedApproves(t *testing.T) {
	ks := newFakeElevationKeystore(t)
	e, dir := newTestInstallElevator(t, ks, func(string) string { return "" })
	enroll(t, ks, dir)

	res, err := e.Elevate(context.Background(), testElevationRequest())
	if err != nil {
		t.Fatalf("Elevate: %v", err)
	}
	if !res.Approved {
		t.Fatal("Approved = false after a real nonce-issue/local-auth-sign/attestation-verify round trip succeeded")
	}
	if !res.Witness.Valid() {
		t.Fatal("Witness.Valid() = false on a genuinely approved elevation -- round-1 CR fix item 9: the retry must carry a real witness")
	}
}
