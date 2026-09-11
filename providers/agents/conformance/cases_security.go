package conformance

// Purpose: the Round-35 security amendment cases (21-T0-RULINGS-R21.md
//
//	§F/§F.2): env allowlist normativeness, the fail-closed pre-spawn
//	secret scan, the no-self-approval rule, data-class propagation, and
//	the credential canary — split from suite.go per the 300-line cap.
//
// Constraints: the seeded canary/secret literals below are split across
//
//	two string literals each so no contiguous credential-shaped match
//	exists in source (GitHub push protection); concatenation still
//	produces the real value the scan under test must catch.
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestChildEnvAllowlist asserts the two normative env allowlists differ
// by exactly the vendorAuthVar, independent of any one driver.
func (s *Suite) TestChildEnvAllowlist(t *testing.T) {
	driver := provider.DriverEnvAllowlist("VENDOR_AUTH")
	tool := provider.ToolEnvAllowlist()
	if len(driver)-len(tool) != 1 {
		t.Fatalf("DriverEnvAllowlist has %d entries, ToolEnvAllowlist has %d: want exactly one more", len(driver), len(tool))
	}
	for _, v := range tool {
		if v == "VENDOR_AUTH" {
			t.Fatal("ToolEnvAllowlist must never carry the vendorAuthVar")
		}
	}
}

// TestPreSpawnSecretScanAborts seeds a credential-shaped value into a
// scratch worktree and asserts PreSpawnScan aborts fail-closed with
// ErrPreSpawnSecretFound and starts no child (a driver's Spawn is not
// exercised here — no implementation ships from this ticket; the pkg-level
// scan seam is the contract under test).
func (s *Suite) TestPreSpawnSecretScanAborts(t *testing.T) {
	root := t.TempDir()
	seeded := "AKIA" + "9QRX2PLM7NZV6WTC"
	if err := os.WriteFile(filepath.Join(root, "seed.env"), []byte("SECRET="+seeded), 0o600); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	if err := provider.PreSpawnScan(root, provider.WorktreeScope{}); err != provider.ErrPreSpawnSecretFound {
		t.Fatalf("PreSpawnScan(seeded) = %v, want ErrPreSpawnSecretFound", err)
	}
}

// TestDriverNeverAutoApproves asserts ApprovalRequests returns a usable
// channel and any request observed on it carries a non-empty ID — a
// driver that resolved its own approval before the caller ever saw it
// would either never surface the request (undetectable generically
// through this interface) or surface a malformed one, which this catches.
func (s *Suite) TestDriverNeverAutoApproves(t *testing.T) {
	p := s.newProvider(t)
	ch := p.ApprovalRequests()
	if ch == nil {
		t.Fatal("ApprovalRequests() returned a nil channel")
	}
	select {
	case req, ok := <-ch:
		if ok && req.ID == "" {
			t.Fatal("ApprovalRequest with an empty ID")
		}
	case <-time.After(50 * time.Millisecond):
		// No approval raised in this window is acceptable: not every job
		// needs one.
	}
}

// TestDataClassPropagates asserts CollectResult's DataClass is never less
// restrictive than the spawning spec's (R-21.143 raise-only rule).
func (s *Suite) TestDataClassPropagates(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	spec := provider.AgentJobSpec{Prompt: "propagate", DataClass: provider.DataClassInternal}
	res, err := p.Spawn(ctx, spec)
	requireNoError(t, err, "Spawn")
	cr, err := p.Collect(ctx, res.JobID)
	requireNoError(t, err, "Collect")
	if provider.JoinDataClass(cr.DataClass, spec.DataClass) != cr.DataClass.Resolved() {
		t.Fatalf("CollectResult.DataClass %q is less restrictive than the spec's %q", cr.DataClass, spec.DataClass)
	}
}

// TestCredentialCanaryFailsClosed seeds a canary value as the job's
// prompt and asserts it never reappears in the persisted CollectResult —
// a driver that echoes its prompt back verbatim into Output or Artifacts
// fails this case, which is the point: it proves the suite is not
// vacuous against a naive pass-through implementation.
func (s *Suite) TestCredentialCanaryFailsClosed(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	canary := "cascade-canary-" + "7f3a9c2e1b8d4f60"
	res, err := p.Spawn(ctx, provider.AgentJobSpec{Prompt: canary, DataClass: provider.DataClassRestricted})
	requireNoError(t, err, "Spawn")
	cr, err := p.Collect(ctx, res.JobID)
	requireNoError(t, err, "Collect")
	if strings.Contains(cr.Output, canary) {
		t.Fatal("CollectResult.Output contains the seeded canary")
	}
	for _, a := range cr.Artifacts {
		if strings.Contains(a, canary) {
			t.Fatal("CollectResult.Artifacts contains the seeded canary")
		}
	}
}
