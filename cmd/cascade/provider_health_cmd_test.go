// Purpose: CLI-surface tests for `cascade provider list|health|remove`
//   (P1-E10-W3-S21-T2), driven against the real S-20.T2 registry
//   (openProviderStorage opens a real SQLite file under t.TempDir() via
//   testProviderDeps' injected PathProvider) rather than a mock -- Art.7.2
//   exempts only the network leg (see provider_usage_cmd_test.go's fake
//   httpDoer for `test`).
//
// FIXED (see journals/FIX-provider-add-list-split.md): `provider add` now
// writes through the same durable registry.Registry via registryAdapter
// (provider_registry_adapter.go) -- provider_add_list_test.go covers the
// add->list/remove path end to end. These tests keep seeding the durable
// registry directly via seedRegistryProvider where the scenario does not
// need `add` itself, since it is the real production entry point either
// way.
// SPORT: provider · J · S-21 · T-2 (P1-E10-W3-S21-T2).

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/provider"
)

// seedRegistryProvider opens the SAME durable registry file
// openProviderStorage will open for deps, and upserts one provider
// record directly -- the real production entry point (registry.Registry),
// not a hand-rolled fake.
func seedRegistryProvider(t *testing.T, deps providerDeps, rec registry.ProviderRecord) {
	t.Helper()
	store, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Registry.UpsertProvider(context.Background(), rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
}

func sampleRegistryProvider(name string, health registry.HealthStatus) registry.ProviderRecord {
	return registry.ProviderRecord{
		Name: name, Driver: registry.DriverAnthropic, BaseURL: "https://api.anthropic.com",
		Auth: registry.AuthKey, AuthRef: registry.VaultKeyRef("provider." + name + ".key"),
		KnownModels: []string{"claude-3-5-sonnet-20241022"}, AccountKind: registry.AccountPersonal,
		Tier: registry.TierStrong, HealthStatus: health,
		Capabilities: provider.Capabilities{Search: provider.CapabilitySupported},
	}
}

func TestProviderList_EndToEnd_NoCredentialLeak(t *testing.T) {
	deps := testProviderDeps(t, nil)
	seedRegistryProvider(t, deps, sampleRegistryProvider("myclaude", registry.HealthHealthy))

	stdout, _, err := runProvider(t, deps, "list", "--json")
	if err != nil {
		t.Fatalf("provider list: %v", err)
	}
	if !strings.Contains(stdout, "myclaude") {
		t.Fatalf("expected myclaude in list output, got %q", stdout)
	}
	if strings.Contains(stdout, "sk-ant") {
		t.Fatal("a credential-shaped value leaked into `provider list --json` output")
	}
}

func TestProviderList_Empty(t *testing.T) {
	deps := testProviderDeps(t, nil)
	stdout, _, err := runProvider(t, deps, "list")
	if err != nil {
		t.Fatalf("provider list: %v", err)
	}
	if !strings.Contains(stdout, "no providers registered") {
		t.Fatalf("expected empty-registry message, got %q", stdout)
	}
}

func TestProviderHealth_UnhealthyExitsNonZero(t *testing.T) {
	deps := testProviderDeps(t, nil)
	seedRegistryProvider(t, deps, sampleRegistryProvider("dead-one", registry.HealthDead))

	_, _, err := runProvider(t, deps, "health")
	if err == nil {
		t.Fatal("expected a non-nil error when a registered provider is unhealthy")
	}
}

func TestProviderHealth_AllHealthy_Succeeds(t *testing.T) {
	deps := testProviderDeps(t, nil)
	seedRegistryProvider(t, deps, sampleRegistryProvider("ok-one", registry.HealthHealthy))

	if _, _, err := runProvider(t, deps, "health"); err != nil {
		t.Fatalf("provider health: unexpected error: %v", err)
	}
}

func TestProviderRemove_IdempotentOnAbsentProvider(t *testing.T) {
	deps := testProviderDeps(t, nil)
	stdout, _, err := runProvider(t, deps, "remove", "never-existed", "--json")
	if err != nil {
		t.Fatalf("provider remove (absent): unexpected error: %v", err)
	}
	if !strings.Contains(stdout, `"delta": "none"`) {
		t.Fatalf("expected delta=none for an already-absent provider, got %q", stdout)
	}
}

func TestProviderRemove_RemovesThenIdempotent(t *testing.T) {
	deps := testProviderDeps(t, nil)
	seedRegistryProvider(t, deps, sampleRegistryProvider("to-remove", registry.HealthHealthy))

	stdout, _, err := runProvider(t, deps, "remove", "to-remove", "--json")
	if err != nil {
		t.Fatalf("provider remove: unexpected error: %v", err)
	}
	if !strings.Contains(stdout, `"delta": "removed"`) {
		t.Fatalf("expected delta=removed, got %q", stdout)
	}

	// Second removal of the same (now-absent) provider must be a no-op,
	// never an error (R-14.95).
	stdout2, _, err2 := runProvider(t, deps, "remove", "to-remove", "--json")
	if err2 != nil {
		t.Fatalf("provider remove (second time): unexpected error: %v", err2)
	}
	if !strings.Contains(stdout2, `"delta": "none"`) {
		t.Fatalf("expected delta=none on the second removal, got %q", stdout2)
	}

	// list must no longer show it.
	listOut, _, err := runProvider(t, deps, "list")
	if err != nil {
		t.Fatalf("provider list: %v", err)
	}
	if strings.Contains(listOut, "to-remove") {
		t.Fatal("removed provider still appears in `provider list`")
	}
}

func TestProviderUsage_EmptyRegistry(t *testing.T) {
	deps := testProviderDeps(t, nil)
	stdout, _, err := runProvider(t, deps, "usage")
	if err != nil {
		t.Fatalf("provider usage: unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "no usage recorded") {
		t.Fatalf("expected no-usage message, got %q", stdout)
	}
}
