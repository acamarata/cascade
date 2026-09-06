package main

// Purpose: covers the grant verb and the standing subtree — the write
//   dispatch, the platform gate and each view's rendering.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingWriter records which write wrapper the command dispatched to.
type recordingWriter struct{ called string }

func (w *recordingWriter) StandingGrantCreate(context.Context, policy.StandingWriteParams) (policy.StandingWriteResult, error) {
	w.called = "create"
	return policy.StandingWriteResult{GrantID: "g1", Action: "workspace.write", Changed: true}, nil
}

func (w *recordingWriter) StandingGrantChange(context.Context, policy.StandingWriteParams) (policy.StandingWriteResult, error) {
	w.called = "change"
	return policy.StandingWriteResult{GrantID: "g1", Action: "workspace.write", Changed: true}, nil
}

// TestStandingWriteCallDispatchesByVerb proves create and change reach
// different wrappers, and that a verb neither function names is refused
// rather than defaulted to the creating call.
func TestStandingWriteCallDispatchesByVerb(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	for _, verb := range []string{"create", "change"} {
		w := &recordingWriter{}
		if _, err := standingWriteCall(cmd, w, verb, policy.StandingWriteParams{}); err != nil {
			t.Fatalf("standingWriteCall(%q): %v", verb, err)
		}
		if w.called != verb {
			t.Errorf("standingWriteCall(%q) dispatched to %q", verb, w.called)
		}
	}
	w := &recordingWriter{}
	if _, err := standingWriteCall(cmd, w, "delete", policy.StandingWriteParams{}); err == nil {
		t.Error("standingWriteCall accepted a verb that is not a standing-grant write")
	} else if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("the refusal kind = %v, want invalid-input", err)
	}
	if w.called != "" {
		t.Errorf("an unknown verb still dispatched to %q", w.called)
	}
}

// TestStandingViewsRender covers each rendering, empty cases included.
func TestStandingViewsRender(t *testing.T) {
	if got := standingListView(policy.StandingListResult{}).String(); !strings.Contains(got, "no standing grants") {
		t.Errorf("the empty list renders as %q", got)
	}
	filled := standingListView(policy.StandingListResult{Grants: []policy.Grant{
		{Capability: "fs.write", ExpiresAt: time.Unix(0, 0).UTC()},
		{Capability: "fs.read", ExpiresAt: time.Unix(0, 0).UTC()},
	}})
	out := filled.String()
	if !strings.Contains(out, "fs.write") || !strings.Contains(out, "fs.read") {
		t.Errorf("the grant list is missing a row:\n%s", out)
	}
	if got := standingWriteView(policy.StandingWriteResult{}).String(); !strings.Contains(got, "no standing grant was written") {
		t.Errorf("an unchanged write renders as %q", got)
	}
	written := standingWriteView(policy.StandingWriteResult{GrantID: "g1", Action: "a", Changed: true})
	if !strings.Contains(written.String(), "g1") {
		t.Errorf("the write view does not name the row: %q", written.String())
	}
	if got := revokeView(policy.StandingWriteResult{Action: "fs.write"}).String(); !strings.Contains(got, "fs.write") {
		t.Errorf("the revoke view does not name what was revoked: %q", got)
	}
	grant := grantView(policy.ApprovalGrantResult{
		RequestID: "req-1", Capability: "fs.write", Level: "L2", ConsumedAt: "1970-01-01T00:00:00Z",
	})
	if !strings.Contains(grant.String(), "req-1") {
		t.Errorf("the grant view does not name the redeemed request: %q", grant.String())
	}
}

// TestStandingCommandsCarryTheirFlags proves each verb declares the flags
// its params need, so a documented flag cannot go missing unnoticed.
func TestStandingCommandsCarryTheirFlags(t *testing.T) {
	deps := approvalDeps{}
	cases := map[*cobra.Command][]string{
		newStandingWriteCmd(deps, "create"): {"grant-id", "action", "capability", "scope", "action-class", "ttl", "subject-kind", "subject-id"},
		newStandingRevokeCmd(deps):          {"capability", "subject-kind", "subject-id"},
		newStandingListCmd(deps):            {"subject-kind", "subject-id"},
		newApprovalGrantCmd(deps):           {"token", "action"},
	}
	for cmd, flags := range cases {
		for _, name := range flags {
			if cmd.Flags().Lookup(name) == nil {
				t.Errorf("cascade approval %s does not declare --%s", cmd.Use, name)
			}
		}
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
