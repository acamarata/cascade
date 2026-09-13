//go:build !windows

// Purpose: the shared-authorizer success-path proof that needs the full
// real elevation ceremony to actually complete. That ceremony is
// architecturally unreachable on Windows -- platformElevationRefusal
// (internal/rpc/elevation_windows.go) preempts it before the handler ever
// runs (internal/rpc/elevation_flow_windows_test.go) -- so this test
// carries the same `!windows` tag as the POSIX attestation flow itself
// (elevation_unix.go), per the AGENT-BRIEF's "build tags on test files"
// rule. backup_windows_tier2_test.go is this package's Windows-side proof
// of the refusal (R-14.131).
// SPORT: cmd.cascade.backup-elevation/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestBackupAuthorizerRealAttestationVerifies is the success-path proof:
// with --yes, a real ed25519 signature from an enrolled key round-trips
// through the nonce challenge, rpc.ElevationMiddleware's real verification,
// and the single-use ledger, minting a non-empty ElevationProof. Swapping
// the enrolled public key for an unrelated one
// (TestBackupAuthorizerRejectsUnenrolledKey, backup_elevation_test.go)
// proves the verification is real, not a hardcoded pass.
func TestBackupAuthorizerRealAttestationVerifies(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	deps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	authorize := newBackupAuthorizer(deps)
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	proof, err := authorize(t.Context(), cmd, "backup.create", []byte(`{"target":"t"}`), true)
	if err != nil {
		t.Fatalf("authorize with --yes and a real enrolled signature: %v", err)
	}
	if proof == "" {
		t.Fatal("authorize returned an empty proof on a verified attestation")
	}
}
