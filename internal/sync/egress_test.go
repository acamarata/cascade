package sync

import (
	"bytes"
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage"
)

func TestSyncEgressClassRegisteredAtInit(t *testing.T) {
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassSync)
	if !ok {
		t.Fatal("EgressClassSync must be registered on the default registry by internal/sync's init")
	}
	if !cfg.Enabled {
		t.Fatal("the sync egress class must be enabled")
	}
	if cfg.AllowRestricted {
		t.Fatal("the sync egress class must not admit restricted content directly; filter.go is the pre-serialization gate")
	}
	if cfg.Owner == "" {
		t.Fatal("the sync egress class must name its owning ticket")
	}
}

func TestSyncEgressClassIssuesCapability(t *testing.T) {
	token, err := egress.DefaultRegistry().Capability(egress.EgressClassSync)
	if err != nil {
		t.Fatalf("Capability: %v", err)
	}
	if token.Class() != egress.EgressClassSync {
		t.Fatalf("Class() = %q, want %q", token.Class(), egress.EgressClassSync)
	}
}

// redTeamVault is a hermetic egress.Vault double: it holds exactly what
// the test put in it and never touches a real vault.
type redTeamVault struct{ values map[string][]byte }

func (v redTeamVault) List(context.Context) ([]string, error) {
	names := make([]string, 0, len(v.values))
	for name := range v.values {
		names = append(names, name)
	}
	return names, nil
}
func (v redTeamVault) Get(_ context.Context, name string) ([]byte, error) { return v.values[name], nil }

// syncRedTeamValue is a synthetic credential-shaped value that exists
// nowhere else, split so no contiguous match reaches this source file
// (AGENT-BRIEF's CREDENTIAL-SHAPED FIXTURES rule).
const syncRedTeamName = "SK_RED_TEAM_SYNC_0001"

func syncRedTeamValue() string { return "sk-rts-" + "AAAA1234567890CCCCdd" }

// TestEgressSyncRedTeam extends the socket-layer red team (06 §5.17) to
// the sync engine's outbound leg (this ticket's own required extension —
// CONTRADICTION: the contract names "internal/secrets/red_team_test.go",
// which does not exist; the real socket-layer red team lives in
// internal/hooks/egress/red_team_test.go. That file is white-box
// `package egress` and importing internal/sync from it is a genuine
// import cycle (internal/sync imports internal/hooks/egress); an
// external `egress_test` file sidesteps the cycle but then pollutes
// internal/hooks/egress's OWN test binary via internal/sync's init-time
// DefaultRegistry() registration, breaking
// TestEgressClassesR21265Registered's exact-count assertion there. This
// test lives in internal/sync instead, which already imports egress with
// no cycle and whose own init-time registration cannot affect any other
// package's test invariants.) It proves: (1) the vault is structurally
// excluded — a record over the vault domain is refused by Admit and never
// reaches Intercept at all, regardless of its declared tier; (2) a
// same-shaped record from a real synced domain still passes through this
// package's substitution pass on the sync class exactly like every other
// registered class.
func TestEgressSyncRedTeam(t *testing.T) {
	secretValue := syncRedTeamValue()
	vaultRecord := Record{Domain: storage.DomainSecrets, Subkind: "vault", ID: "rt-vault-1", Tier: egress.TierPublic, Payload: []byte(secretValue)}
	if res := Admit(vaultRecord); res.Admitted {
		t.Fatal("a record over the vault domain must never be admitted by the sync filter, at any declared tier")
	}

	memoryRecord := Record{Domain: storage.DomainMemory, Subkind: "memory", ID: "rt-memory-1", Tier: egress.TierInternal, Payload: []byte("note containing " + secretValue + " inline")}
	res := Admit(memoryRecord)
	if !res.Admitted {
		t.Fatalf("a synced-domain internal-tier record must be admitted, got Reason=%q", res.Reason)
	}

	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	registry := egress.NewRegistry()
	if err := registry.Register(egress.EgressClassSync, egress.InterceptConfig{Enabled: true, Owner: "test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	vault := redTeamVault{values: map[string][]byte{syncRedTeamName: []byte(secretValue)}}
	eng, err := egress.NewEngine(registry, vault, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	out, err := eng.InterceptClass(context.Background(), egress.EgressClassSync, egress.TierInternal, memoryRecord.Payload)
	if err != nil {
		t.Fatalf("InterceptClass(EgressClassSync): %v", err)
	}
	if bytes.Contains(out, []byte(secretValue)) {
		t.Fatalf("the synthetic secret is still present on the sync class's outbound bytes: %q", out)
	}
}
