// Purpose: tests for agent_types.go's DTOs — AgentRunState's closed
//
//	vocabulary and no-permissive-zero-value rule, the DataClass lattice's
//	JoinDataClass/WithDataClass raise-only rule, EgressClassAgentDriver,
//	and the CompliancePosture.CredentialSharing compile-time constraint
//	(already enforced by compliance.go — reasserted here from the
//	AgentProvider surface per this ticket's task list).
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) — P1-E30-W6-S61-T1.
package provider_test

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestAgentRunStateValid(t *testing.T) {
	valid := []provider.AgentRunState{
		provider.AgentRunPending, provider.AgentRunLeased, provider.AgentRunRunning,
		provider.AgentRunVerifying, provider.AgentRunReviewing, provider.AgentRunAccepted,
		provider.AgentRunRejected, provider.AgentRunCancelling, provider.AgentRunCancelled,
		provider.AgentRunFailed,
	}
	if len(valid) != 10 {
		t.Fatalf("expected ten AgentRunState members, table has %d", len(valid))
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Fatalf("%q.Valid() = false, want true", s)
		}
	}
}

func TestAgentRunStateCancelling(t *testing.T) {
	if provider.AgentRunState("").Valid() {
		t.Fatal("zero value AgentRunState reports Valid() = true, want false (no permissive zero value)")
	}
	if !provider.AgentRunCancelling.Valid() {
		t.Fatal("AgentRunCancelling.Valid() = false, want true")
	}
	if provider.AgentRunCancelling.Terminal() {
		t.Fatal("AgentRunCancelling.Terminal() = true, want false: it is the sole gateway into cancelled")
	}
	if !provider.AgentRunCancelled.Terminal() {
		t.Fatal("AgentRunCancelled.Terminal() = false, want true")
	}
}

func TestDataClassJoinMonotonic(t *testing.T) {
	cases := []struct {
		a, b, want provider.DataClass
	}{
		{provider.DataClassPublic, provider.DataClassInternal, provider.DataClassInternal},
		{provider.DataClassRestricted, provider.DataClassPublic, provider.DataClassRestricted},
		{provider.DataClassLocalOnly, provider.DataClassRestricted, provider.DataClassLocalOnly},
		{"", provider.DataClassPublic, provider.DataClassRestricted}, // unresolved -> restricted
	}
	for _, c := range cases {
		got := provider.JoinDataClass(c.a, c.b)
		if got != c.want {
			t.Errorf("JoinDataClass(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestDataClassDowngradeRejected(t *testing.T) {
	spec := provider.AgentJobSpec{DataClass: provider.DataClassRestricted}
	if _, err := spec.WithDataClass(provider.DataClassPublic); err != provider.ErrDataClassDowngrade {
		t.Fatalf("WithDataClass(downgrade) err = %v, want ErrDataClassDowngrade", err)
	}
	raised, err := spec.WithDataClass(provider.DataClassLocalOnly)
	if err != nil {
		t.Fatalf("WithDataClass(raise): %v", err)
	}
	if raised.DataClass != provider.DataClassLocalOnly {
		t.Fatalf("WithDataClass(raise) = %q, want local-only", raised.DataClass)
	}
}

func TestDataClassResolvedUnknownIsRestricted(t *testing.T) {
	if got := provider.DataClass("bogus").Resolved(); got != provider.DataClassRestricted {
		t.Fatalf("Resolved(unknown) = %q, want restricted", got)
	}
}

func TestEgressClassAgentDriverNonEmpty(t *testing.T) {
	if provider.EgressClassAgentDriver == "" {
		t.Fatal("EgressClassAgentDriver is empty")
	}
}

func TestCompliancePostureCredentialSharingConstraint(t *testing.T) {
	// CompliancePosture already lives in compliance.go (R-16.10); it
	// applies to AgentProvider exactly as it does to ModelProvider. The
	// only exported constructor never accepts a value other than
	// CredentialSharingForbidden.
	p := provider.NewCompliancePosture([]string{"oauth"}, true, true, []string{"agent"}, "steady", true)
	if p.CredentialSharing != provider.CredentialSharingForbidden {
		t.Fatalf("CredentialSharing = %q, want forbidden", p.CredentialSharing)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestDriverDefaultsValues(t *testing.T) {
	d := provider.NewDriverDefaults()
	if d.CancelGrace.Seconds() != 30 {
		t.Errorf("CancelGrace = %v, want 30s", d.CancelGrace)
	}
	if d.ApprovalTimeout.Minutes() != 30 {
		t.Errorf("ApprovalTimeout = %v, want 30m", d.ApprovalTimeout)
	}
	if d.PollInterval.Seconds() != 5 {
		t.Errorf("PollInterval = %v, want 5s", d.PollInterval)
	}
}

func TestCapacitySnapshotZeroStateUnknown(t *testing.T) {
	var snap provider.CapacitySnapshot
	if snap.State != provider.CapacityStateUnknown {
		t.Fatalf("zero CapacitySnapshot.State = %q, want unknown", snap.State)
	}
}
