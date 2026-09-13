//go:build windows

// Purpose: proves, on the Windows lane itself (R-14.131: a platform-
// specific behavior needs a test that RUNS on that platform, not merely
// builds), that newBackupAuthorizer's real-attestation ceremony refuses
// cleanly with elevation.ErrWindowsTier2 when platformElevationRefusal
// (internal/rpc/elevation_windows.go) preempts it -- even when the
// injected keystore fake reports a non-tier2 Tier() (TierOSKeychain), the
// shape every POSIX-only real-ceremony test in this package uses. Before
// the fix (issueBackupChallenge, backup_elevation.go), this exact case
// mislabeled the platform's by-design nonce-less refusal
// (internal/rpc/elevation_flow_windows_test.go) as
// `cascade.KindIntegrity "backup: elevation challenge has no nonce"` --
// the CI failure this file's fix and the ...realceremony_test.go build-tag
// split together resolve.
// SPORT: cmd.cascade.backup-elevation/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBackupAuthorizerWindowsTier2EvenWithNonTier2Keystore(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	deps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	authorize := newBackupAuthorizer(deps)
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))

	_, err := authorize(t.Context(), cmd, "backup.create", []byte(`{"target":"t"}`), true)
	if !isCLIKind(err, cascade.KindUnsupported) {
		t.Fatalf("authorize on Windows with --yes and a real signature = %v, want the ErrWindowsTier2 refusal (KindUnsupported)", err)
	}
	if strings.Contains(err.Error(), "no nonce") {
		t.Fatalf("authorize on Windows leaked the nonce-less-challenge integrity message instead of the tier-2 refusal: %v", err)
	}
}
