//go:build windows

// Purpose: the Windows half of the approval CLI's elevation-gate tests,
//
//	the mirror approval_standing_test.go's and approval_test.go's own
//	doc comments already promised (Windows parity pass 3). No elevation
//	flow exists on Windows at all (approval_platform_windows.go), so
//	grant and standing create/change refuse locally, before any dial,
//	with cascade.KindUnsupported — a genuinely different observable
//	result than the POSIX mirror in approval_elevation_unix_test.go,
//	never a weakened or skipped version of it. Standing list and
//	standing revoke are NOT elevated verbs (see approval_standing.go's
//	own Constraints comment) and keep reaching a real, unreachable
//	socket exactly as they do on POSIX.
//
// SPORT: cli/approval-standing/ADD (P1-E09-W2-S18-T6), windows-parity-pass-3.
package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestElevatedPlatformGate_RefusesOnWindows is the Windows mirror of
// approval_elevation_unix_test.go's TestElevatedPlatformGate: there the
// gate is a no-op; here it always refuses, and names why.
func TestElevatedPlatformGate_RefusesOnWindows(t *testing.T) {
	err := refuseElevatedOnUnsupportedPlatform("approval grant")
	if err == nil {
		t.Fatal("the platform gate returned nil on Windows, where no elevation flow exists")
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("refusal kind = %v, want unsupported", err)
	}
	if !strings.Contains(err.Error(), "approval grant") {
		t.Errorf("the refusal does not name the verb it refused: %v", err)
	}
}

// TestApprovalGrantRequiresTheToken_RefusesForElevationOnWindows is the
// Windows mirror of approval_elevation_unix_test.go's
// TestApprovalGrantRequiresTheToken: on POSIX a missing --token is what
// grant refuses on; on Windows the elevation gate refuses first, so a
// TOKEN IS PRESENT here specifically to isolate that this is the gate
// firing, not a coincidental token-shaped complaint.
func TestApprovalGrantRequiresTheToken_RefusesForElevationOnWindows(t *testing.T) {
	out, err := execRoot(t, "approval", "grant", "01J8ZC5W2K4F6H8M0P2R4T6V8X", "--token", "signed-token-value")
	if err == nil {
		t.Fatalf("approval grant succeeded on Windows, where no elevation flow exists: %s", out)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("refusal kind = %v, want unsupported (the elevation gate)", err)
	}
	if strings.Contains(err.Error(), "signed approval token") {
		t.Errorf("refused for the missing token, not the elevation gate: %v", err)
	}
}

// TestStandingCommands_ElevatedVerbsRefuseBeforeDial_Windows is the
// Windows mirror of approval_elevation_unix_test.go's
// TestStandingCommands_TransportFailurePropagates for grant and standing
// create ONLY (the two elevated cases there): on Windows both refuse via
// the platform gate before approvalClient ever dials, so the transport
// failure that test proves on POSIX cannot occur here.
func TestStandingCommands_ElevatedVerbsRefuseBeforeDial_Windows(t *testing.T) {
	cases := []struct {
		name string
		cmd  func(approvalDeps) *cobra.Command
		args []string
	}{
		{"grant", func(d approvalDeps) *cobra.Command { return newApprovalGrantCmd(d) },
			[]string{"01J8ZC5W2K4F6H8M0P2R4T6V8X", "--token", "signed-token-value"}},
		{"standing create", func(d approvalDeps) *cobra.Command { return newStandingWriteCmd(d, "create") },
			[]string{"--grant-id", "01J8ZC5W2K4F6H8M0P2R4T6V8X", "--capability", "fs.write"}},
	}
	for _, tc := range cases {
		deps := hermeticApprovalDeps(t)
		out, err := execLeaf(t, tc.cmd(deps), tc.args)
		if err == nil {
			t.Fatalf("approval %s succeeded on Windows, where no elevation flow exists: %s", tc.name, out)
		}
		if !cascade.HasKind(err, cascade.KindUnsupported) {
			t.Errorf("approval %s error kind = %v, want unsupported (the elevation gate)", tc.name, err)
		}
		if strings.Contains(err.Error(), "daemon not running or unreachable") {
			t.Errorf("approval %s reached the transport dial; the elevation gate should have refused first", tc.name)
		}
	}
}

// TestStandingCommands_NonElevatedTransportFailurePropagates_Windows
// proves standing list and standing revoke, which are NOT elevated
// verbs, still reach a real dial on Windows and fail exactly as they do
// on POSIX (approval_elevation_unix_test.go's own two non-elevated
// cases) — the platform split above is narrow, not a blanket Windows
// bypass of the whole command group.
func TestStandingCommands_NonElevatedTransportFailurePropagates_Windows(t *testing.T) {
	cases := []struct {
		name string
		cmd  func(approvalDeps) *cobra.Command
		args []string
	}{
		{"standing list", newStandingListCmd, nil},
		{"standing revoke", newStandingRevokeCmd, []string{"--capability", "fs.write"}},
	}
	for _, tc := range cases {
		deps := hermeticApprovalDeps(t)
		out, err := execLeaf(t, tc.cmd(deps), tc.args)
		if err == nil {
			t.Fatalf("approval %s succeeded against a socket nothing listens on: %s", tc.name, out)
		}
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("approval %s error kind = %v, want unavailable", tc.name, err)
		}
		if !strings.Contains(err.Error(), "daemon not running or unreachable") {
			t.Errorf("approval %s error = %v, want it to name the unreachable daemon", tc.name, err)
		}
	}
}
