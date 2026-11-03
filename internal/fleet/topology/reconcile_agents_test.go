package topology

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestReconcileAgentLanes(t *testing.T) {
	fixtures := []struct {
		harness string
		runtime RuntimeKind
	}{
		{"claude-cli", RuntimeClaudeCLI}, {"codex", RuntimeCodex},
		{"opencode", RuntimeOpenCode}, {"antigravity", RuntimeAntigravity},
	}
	for _, f := range fixtures {
		t.Run(f.harness, func(t *testing.T) { checkAgentRows(t, f.harness, f.runtime) })
	}
	t.Run("unknown harness writes nothing", checkAgentUnknown)
	t.Run("owner columns preserved", checkAgentOwners)
	t.Run("write failure rolls back", checkAgentFailure)
}

func checkAgentRows(t *testing.T, harness string, runtime RuntimeKind) {
	ctx, s := context.Background(), seedTicketStore(t)
	records := []AgentLaneRecord{{LaneName: "lane-a", Harness: harness}}
	if n, err := ReconcileAgentLanes(ctx, s, newTestClock(), records); n != 1 || err != nil {
		t.Fatalf("reconcile = %d, %v", n, err)
	}
	id := "agent:" + harness
	a, err := s.GetAccount(ctx, AccountID(id))
	if err != nil || a.Provider != "agent" || a.Billing.Kind != BillingFree || a.Role != AccountRoleWorkforce {
		t.Fatalf("account = %+v (%v)", a, err)
	}
	d, err := s.GetQuotaDomain(ctx, DomainID(id))
	if err != nil || d.AccountRef != a.ID || d.Kind != QuotaDomainAPIProject || d.BillingTier != BillingTierDiscover {
		t.Fatalf("domain = %+v (%v)", d, err)
	}
	p, err := s.GetRuntimeProfile(ctx, RuntimeProfileID(id))
	if err != nil || p.Runtime != runtime || !p.Persistent {
		t.Fatalf("profile = %+v (%v)", p, err)
	}
	c, err := s.GetCredential(ctx, CredentialID(id))
	if err != nil || c.AccountRef != a.ID || c.QuotaDomainRef != d.ID || c.RuntimeProfileRef != p.ID ||
		c.Health != CredentialOK || c.SecretRef != "" {
		t.Fatalf("credential = %+v (%v)", c, err)
	}
	if got, err := s.DomainForCredential(ctx, c.ID); got != d.ID || err != nil {
		t.Fatalf("domain lookup = %s, %v", got, err)
	}
	before := mustSnapshot(ctx, t, s)
	if n, err := ReconcileAgentLanes(ctx, s, newTestClock(), records); n != 1 || err != nil {
		t.Fatalf("repeat reconcile = %d, %v", n, err)
	}
	if !reflect.DeepEqual(before, mustSnapshot(ctx, t, s)) {
		t.Fatal("repeat reconcile changed rows")
	}
}

func checkAgentUnknown(t *testing.T) {
	ctx, s := context.Background(), seedTicketStore(t)
	before := mustSnapshot(ctx, t, s)
	records := []AgentLaneRecord{{LaneName: "lane-a", Harness: string(RuntimeCodex)}, {LaneName: "lane-b", Harness: "future-harness"}}
	n, err := ReconcileAgentLanes(ctx, s, newTestClock(), records)
	assertSeedInvariant(t, err, `agent_harness "future-harness": unknown harness id`)
	if n != 0 || !reflect.DeepEqual(before, mustSnapshot(ctx, t, s)) {
		t.Fatal("unknown harness changed rows")
	}
	n, err = ReconcileAgentLanes(ctx, s, newTestClock(), []AgentLaneRecord{{Harness: string(RuntimeCodex)}})
	assertSeedInvariant(t, err, "lane name must not be empty")
	if n != 0 || !reflect.DeepEqual(before, mustSnapshot(ctx, t, s)) {
		t.Fatal("empty lane name changed rows")
	}
	if n, err := ReconcileAgentLanes(ctx, s, newTestClock(), records[:1]); n != 1 || err != nil {
		t.Fatalf("valid harness = %d, %v", n, err)
	}
}

func checkAgentOwners(t *testing.T) {
	ctx, s := context.Background(), seedTicketStore(t)
	records := []AgentLaneRecord{{LaneName: "lane-a", Harness: string(RuntimeCodex)}}
	if _, err := ReconcileAgentLanes(ctx, s, newTestClock(), records); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetAccount(ctx, "agent:codex")
	a.Role, a.Billing.UserReportedMonthlyMicros = AccountRoleExecutive, 42
	if err := s.UpsertAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetRuntimeProfile(ctx, "agent:codex")
	p.ConfigHome, p.Persistent = t.TempDir(), false
	if err := s.UpsertRuntimeProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	d, _ := s.GetQuotaDomain(ctx, "agent:codex")
	d.BillingTier, d.Quarantined = BillingTierPaid, true
	if err := s.UpsertQuotaDomain(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileAgentLanes(ctx, s, newTestClock(), records); err != nil {
		t.Fatal(err)
	}
	after := mustSnapshot(ctx, t, s)
	if after.Accounts[0] != a || after.RuntimeProfiles[0] != p || !reflect.DeepEqual(after.QuotaDomains[0], d) ||
		after.Lanes[0].Health != LaneHealthQuarantined {
		t.Fatalf("owner fields overwritten: %+v", after)
	}
}

func checkAgentFailure(t *testing.T) {
	ctx, s := context.Background(), seedTicketStore(t)
	records := []AgentLaneRecord{{LaneName: "lane-a", Harness: string(RuntimeCodex)}}
	before := mustSnapshot(ctx, t, s)
	for _, table := range []string{tableAccount, tableQuotaDomain, tableRuntimeProfile, tableCredential, tableLane} {
		mustExec(ctx, t, s, `CREATE TRIGGER agent_fail BEFORE INSERT ON `+table+` BEGIN SELECT RAISE(ABORT, 'injected agent failure'); END`)
		n, err := ReconcileAgentLanes(ctx, s, newTestClock(), records)
		if n != 0 || err == nil || !strings.Contains(err.Error(), "injected agent failure") {
			t.Fatalf("failed pass = %d, %v", n, err)
		}
		if !reflect.DeepEqual(before, mustSnapshot(ctx, t, s)) {
			t.Fatalf("failed %s write changed rows", table)
		}
		mustExec(ctx, t, s, `DROP TRIGGER agent_fail`)
	}
	if n, err := ReconcileAgentLanes(ctx, s, newTestClock(), records); n != 1 || err != nil {
		t.Fatalf("recovered pass = %d, %v", n, err)
	}
}
