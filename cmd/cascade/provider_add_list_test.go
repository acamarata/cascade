// Purpose: end-to-end proof for the provider add/list split fix
//   (P1-E10-W3-S21-T2's journal, "TWO SEPARATE, disconnected registries"):
//   `cascade provider add` and `cascade provider list/remove` now read and
//   write the SAME durable store. Every test here leaves deps.Registry nil
//   (testProviderDepsDurable), matching production's own lazy resolution
//   (resolveProviderRegistry, provider_cmd.go) rather than the isolated
//   MemoryRegistry provider_cmd_test.go's other tests inject.
// SPORT: cli.provider.add/ADD (P1-E10-W3-S20-T1 follow-up).

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
)

// testProviderDepsDurable is testProviderDeps with the MemoryRegistry seam
// removed, forcing every RunE to resolve through resolveProviderRegistry's
// production path (openProviderStorage against deps.Paths' real,
// per-test-temp-dir SQLite file).
func testProviderDepsDurable(t *testing.T) providerDeps {
	t.Helper()
	deps := testProviderDeps(t, nil)
	deps.Registry = nil
	return deps
}

func TestProviderAddThenList_SameProcess(t *testing.T) {
	deps := testProviderDepsDurable(t)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify"); err != nil {
		t.Fatalf("provider add: %v", err)
	}
	stdout, _, err := runProvider(t, deps, "list")
	if err != nil {
		t.Fatalf("provider list: %v", err)
	}
	if !strings.Contains(stdout, "myclaude") {
		t.Fatalf("added provider did not appear in `provider list`, got %q", stdout)
	}
}

// TestProviderAddThenList_AfterFreshRegistryReopen is the durability
// proof: it opens a brand-new *registry.Registry handle on the same
// providers.db file (openProviderStorage a second time, never reusing the
// handle `add` wrote through) and asserts the record survives -- durable,
// cross-invocation persistence, not merely a shared in-process value.
func TestProviderAddThenList_AfterFreshRegistryReopen(t *testing.T) {
	deps := testProviderDepsDurable(t)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify"); err != nil {
		t.Fatalf("provider add: %v", err)
	}

	store, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage (fresh handle): %v", err)
	}
	rec, err := store.Registry.GetProvider(context.Background(), "myclaude")
	if err != nil {
		_ = store.Close()
		t.Fatalf("GetProvider on a fresh handle: %v", err)
	}
	if rec.Name != "myclaude" || rec.Driver != registry.DriverAnthropic {
		_ = store.Close()
		t.Fatalf("provider did not survive a fresh registry handle: %+v", rec)
	}
	// Close before the next openProviderStorage call (`list`): the events
	// store enforces single-writer exclusivity, so two open handles at
	// once would refuse rather than prove anything about durability.
	if err := store.Close(); err != nil {
		t.Fatalf("close fresh handle: %v", err)
	}

	stdout, _, err := runProvider(t, deps, "list")
	if err != nil {
		t.Fatalf("provider list: %v", err)
	}
	if !strings.Contains(stdout, "myclaude") {
		t.Fatalf("added provider did not appear in `provider list` after a fresh reopen, got %q", stdout)
	}
}

func TestProviderAddThenRemoveThenList_EndToEnd(t *testing.T) {
	deps := testProviderDepsDurable(t)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify"); err != nil {
		t.Fatalf("provider add: %v", err)
	}
	if stdout, _, err := runProvider(t, deps, "list"); err != nil || !strings.Contains(stdout, "myclaude") {
		t.Fatalf("provider list before remove: stdout=%q err=%v", stdout, err)
	}

	if _, _, err := runProvider(t, deps, "remove", "myclaude"); err != nil {
		t.Fatalf("provider remove: %v", err)
	}
	stdout, _, err := runProvider(t, deps, "list")
	if err != nil {
		t.Fatalf("provider list after remove: %v", err)
	}
	if strings.Contains(stdout, "myclaude") {
		t.Fatalf("removed provider still appears in `provider list`, got %q", stdout)
	}
}

// TestProviderAddReAddPreservesHealthAndTier proves mergeIntakeOntoRegistry
// Record (provider_registry_adapter.go) does not reset registry-only
// fields intake does not own: a re-add must never silently demote a
// healthy, tiered provider back to unknown/default.
func TestProviderAddReAddPreservesHealthAndTier(t *testing.T) {
	deps := testProviderDepsDurable(t)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify"); err != nil {
		t.Fatalf("first add: %v", err)
	}

	store, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage: %v", err)
	}
	rec, err := store.Registry.GetProvider(context.Background(), "myclaude")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	rec.Tier = registry.TierStrongest
	rec.HealthStatus = registry.HealthHealthy
	if err := store.Registry.UpsertProvider(context.Background(), rec); err != nil {
		t.Fatalf("seed Tier/HealthStatus: %v", err)
	}
	_ = store.Close()

	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify"); err != nil {
		t.Fatalf("re-add: %v", err)
	}

	store2, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage (2): %v", err)
	}
	defer func() { _ = store2.Close() }()
	got, err := store2.Registry.GetProvider(context.Background(), "myclaude")
	if err != nil {
		t.Fatalf("GetProvider after re-add: %v", err)
	}
	if got.Tier != registry.TierStrongest || got.HealthStatus != registry.HealthHealthy {
		t.Fatalf("re-add reset registry-only fields: Tier=%v HealthStatus=%v", got.Tier, got.HealthStatus)
	}
}

// TestRegistryAdapterRefusesUnknownDriver proves the adapter fails closed
// on an unparseable/unknown DriverKind rather than falling through to a
// default -- registry.ProviderRecord.Validate's Driver.Valid() check,
// inherited unchanged through the type conversion in
// mergeIntakeOntoRegistryRecord.
func TestRegistryAdapterRefusesUnknownDriver(t *testing.T) {
	deps := testProviderDepsDurable(t)
	store, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage: %v", err)
	}
	defer func() { _ = store.Close() }()

	adapter := newRegistryAdapter(store.Registry)
	err = adapter.UpsertProvider(context.Background(), intake.ProviderRecord{
		Name: "bogus", Driver: intake.DriverKind("not-a-real-driver"),
		Auth: intake.AuthKey, AuthRef: intake.VaultKeyRef("provider.bogus.key"),
	})
	if err == nil {
		t.Fatal("expected an unknown driver_kind to be refused")
	}
	if _, gerr := store.Registry.GetProvider(context.Background(), "bogus"); gerr == nil {
		t.Fatal("a refused UpsertProvider must persist nothing")
	}
}
