//go:build !windows

// Purpose: the POSIX half of the approval CLI's elevation-gate tests,
//
//	split out of approval_standing_test.go (Windows parity pass 3): on a
//	platform with an elevation flow, refuseElevatedOnUnsupportedPlatform
//	is a no-op and grant/standing-create proceed to a real dial, which
//	fails against a socket nothing serves. The Windows mirror of both
//	tests lives in approval_windows_test.go.
//
// SPORT: cli/approval-standing/ADD (P1-E09-W2-S18-T6), windows-parity-pass-3.
package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestApprovalGrantRequiresTheToken proves the CLI refuses before it dials
// when the signed token is absent: a request id alone is never enough.
// This is POSIX-only: on Windows, refuseElevatedOnUnsupportedPlatform
// refuses grant before the --token check ever runs, for a different
// reason (see approval_windows_test.go).
func TestApprovalGrantRequiresTheToken(t *testing.T) {
	out, err := execRoot(t, "approval", "grant", "01J8ZC5W2K4F6H8M0P2R4T6V8X")
	if err == nil {
		t.Fatalf("approval grant with no token succeeded: %s", out)
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("the refusal does not name the missing token: %v", err)
	}
}

// TestElevatedPlatformGate pins the gate's behaviour on the platform this
// test binary was built for. On a platform with an elevation flow it
// permits, and the daemon decides; the Windows refusal is asserted by
// approval_windows_test.go, which builds only there.
func TestElevatedPlatformGate(t *testing.T) {
	err := refuseElevatedOnUnsupportedPlatform("approval grant")
	if err != nil {
		t.Fatalf("the platform gate refused on a platform with an elevation flow: %v", err)
	}
}

// TestStandingCommands_TransportFailurePropagates drives grant, standing
// list, standing create/change and standing revoke's REAL RunE past
// approvalClient's success and into the actual client call, which fails to
// dial a socket nothing serves. On POSIX every one of these verbs reaches
// the dial (the elevation decision belongs to the daemon here) — this is
// the "elevation flow exists" half of the platform split; the Windows
// mirror only re-asserts grant and standing create, which refuse before
// ever reaching this failure mode there.
func TestStandingCommands_TransportFailurePropagates(t *testing.T) {
	cases := []struct {
		name string
		cmd  func(approvalDeps) *cobra.Command
		args []string
	}{
		{"grant", func(d approvalDeps) *cobra.Command { return newApprovalGrantCmd(d) },
			[]string{"01J8ZC5W2K4F6H8M0P2R4T6V8X", "--token", "signed-token-value"}},
		{"standing list", newStandingListCmd, nil},
		{"standing create", func(d approvalDeps) *cobra.Command { return newStandingWriteCmd(d, "create") },
			[]string{"--grant-id", "01J8ZC5W2K4F6H8M0P2R4T6V8X", "--capability", "fs.write"}},
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
