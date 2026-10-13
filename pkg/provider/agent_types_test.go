// Purpose: tests for agent_types.go's DTOs — AgentRunState's closed
//
//	vocabulary and no-permissive-zero-value rule, the DataClass lattice's
//	JoinSensitivity/WithDataClass raise-only rule, EgressClassAgentDriver,
//	and the CompliancePosture.CredentialSharing compile-time constraint
//	(already enforced by compliance.go — reasserted here from the
//	AgentProvider surface per this ticket's task list).
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) — P1-E30-W6-S61-T1, S61-T2.
package provider_test

import (
	"regexp"
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
		{provider.SensitivityRestricted, provider.DataClassPublic, provider.DataClassRestricted}, // unresolved -> restricted
	}
	for _, c := range cases {
		got := provider.JoinSensitivity(c.a, c.b)
		if got != c.want {
			t.Errorf("JoinSensitivity(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
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

func TestDataClassUnknownJoinsAsLocalOnly(t *testing.T) {
	if got := provider.JoinSensitivity(provider.SensitivityTier(9), provider.SensitivityPublic); got != provider.SensitivityLocalOnly {
		t.Fatalf("JoinSensitivity(unknown) = %q, want local-only", got)
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

// TestDriverDefaultsSpawnFlagsEmpty asserts every AD/S-61.T2 spawn-flag
// field NewDriverDefaults returns is empty/zero, so the W6 argv omits them.
func TestDriverDefaultsSpawnFlagsEmpty(t *testing.T) {
	d := provider.NewDriverDefaults()
	strs := []string{d.SessionNameFlag, d.KernelPromptFile, d.SubagentKernelPromptFile,
		d.PluginDir, d.Effort, d.FallbackModel, d.MCPConfigPath}
	for i, s := range strs {
		if s != "" {
			t.Fatalf("spawn-flag string %d = %q, want empty", i, s)
		}
	}
	if d.MaxTurns != 0 || d.MaxBudgetUSD != 0 {
		t.Fatalf("MaxTurns=%d MaxBudgetUSD=%v, want zero", d.MaxTurns, d.MaxBudgetUSD)
	}
}

// TestResolveToolPolicyClosedEnum asserts "" resolves to none (fail
// closed), the three members pass through, and anything else refuses.
func TestResolveToolPolicyClosedEnum(t *testing.T) {
	cases := map[string]string{"": provider.ToolPolicyNone, "none": "none", "readonly": "readonly", "all": "all"}
	for in, want := range cases {
		got, err := provider.ResolveToolPolicy(in)
		if err != nil || got != want {
			t.Fatalf("ResolveToolPolicy(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"bogus", "ALL", " none"} {
		got, err := provider.ResolveToolPolicy(bad)
		if err != provider.ErrInvalidToolPolicy || got != "" {
			t.Fatalf("ResolveToolPolicy(%q) = %q, %v; want ErrInvalidToolPolicy", bad, got, err)
		}
	}
}

// TestCollectResultCostIsEstimate pins the cost-source label a driver
// stamps on a CLI-reported cost (R-14.288: an estimate, never billing).
func TestCollectResultCostIsEstimate(t *testing.T) {
	r := provider.CollectResult{Cost: &provider.CostEstimate{USD: 0.5, Source: provider.CostSourceCLIObservation}}
	if r.Cost.Source != "cli-observation" {
		t.Fatalf("cost source = %q", r.Cost.Source)
	}
}

// exeReporter is a minimal ExecutableReporter for the seam test.
type exeReporter string

func (e exeReporter) Executable() string { return string(e) }

// TestAgentJobSpecSpawnSeams pins the R-14.329 spawn seams: the zero spec
// carries none of them (so an external driver's refusals see empty values),
// WithDataClass, the one spec transformer, keeps every seam intact, and a
// driver type satisfies ExecutableReporter through Executable().
func TestAgentJobSpecSpawnSeams(t *testing.T) {
	var zero provider.AgentJobSpec
	if zero.WorktreeRoot != "" || zero.Filter != nil || zero.JobID != "" || zero.TicketID != "" || zero.Env.Driver != nil {
		t.Fatalf("zero AgentJobSpec carries a seam value: %+v", zero)
	}
	filter := func(b []byte) ([]byte, error) { return append([]byte("filtered:"), b...), nil }
	env := provider.SpawnEnv{Driver: []string{"HOME=/h"}, Tool: []string{"HOME=/h"}, Home: "/h"}
	spec := provider.AgentJobSpec{Prompt: "p", DataClass: provider.DataClassInternal, Env: env,
		WorktreeRoot: "/leases/7", Filter: filter, JobID: "job-7", TicketID: "P1-X-T1"}
	raised, err := spec.WithDataClass(provider.DataClassRestricted)
	if err != nil {
		t.Fatalf("WithDataClass: %v", err)
	}
	if raised.WorktreeRoot != "/leases/7" || raised.JobID != "job-7" || raised.TicketID != "P1-X-T1" ||
		raised.Env.Home != "/h" || len(raised.Env.Driver) != 1 || raised.Filter == nil {
		t.Fatalf("WithDataClass dropped a spawn seam: %+v", raised)
	}
	out, err := raised.Filter([]byte("x"))
	if err != nil || string(out) != "filtered:x" {
		t.Fatalf("carried Filter returned %q, %v; want the caller's filter", out, err)
	}
	var r provider.ExecutableReporter = exeReporter("/opt/harness/bin/cli")
	if r.Executable() != "/opt/harness/bin/cli" {
		t.Fatalf("Executable() = %q", r.Executable())
	}
}

func TestSessionIDForIsStableUUID(t *testing.T) {
	uuidRE := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	sessionRE := regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	got := provider.SessionIDFor("P1-TEST-T1")
	if !uuidRE.MatchString(got) {
		t.Fatalf("SessionIDFor = %q, want a lowercase version-5 RFC variant UUID", got)
	}
	if !sessionRE.MatchString(got) {
		t.Fatalf("SessionIDFor = %q does not match the principal session_id pattern", got)
	}
	// Computed independently: python3 -c 'import uuid; print(uuid.uuid5(uuid.NAMESPACE_URL, "P1-TEST-T1"))'.
	if want := "80d82727-9cb0-5414-8065-65636e756556"; got != want {
		t.Fatalf("SessionIDFor(P1-TEST-T1) = %q, want the independently computed %q", got, want)
	}
	if provider.SessionIDFor("P1-TEST-T1") != got {
		t.Fatal("SessionIDFor is not stable for one job id")
	}
	if provider.SessionIDFor("P1-TEST-T2") == got {
		t.Fatal("SessionIDFor returned one id for two job ids")
	}
}
