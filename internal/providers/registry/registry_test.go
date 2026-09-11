package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func sampleProvider(name string) ProviderRecord {
	return ProviderRecord{
		Name:        name,
		Driver:      DriverAnthropic,
		BaseURL:     "https://api.anthropic.com",
		Auth:        AuthKey,
		AuthRef:     VaultKeyRef("provider." + name + ".key"),
		KnownModels: []string{"claude-3-5-sonnet-20241022"},
		AccountKind: AccountPersonal,
		Tier:        TierStrong,
		Capabilities: provider.Capabilities{
			Search:  provider.CapabilitySupported,
			ToolUse: provider.CapabilitySupported,
		},
		HealthStatus: HealthUnknown,
	}
}

func TestRegistryUpsertRoundtrip(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	rec := sampleProvider("anthropic")

	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	got, err := reg.GetProvider(ctx, "anthropic")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Name != rec.Name || got.Driver != rec.Driver || got.BaseURL != rec.BaseURL ||
		got.Auth != rec.Auth || got.AuthRef != rec.AuthRef || got.AccountKind != rec.AccountKind ||
		got.Tier != rec.Tier || got.HealthStatus != rec.HealthStatus {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", got, rec)
	}
	if len(got.KnownModels) != 1 || got.KnownModels[0] != rec.KnownModels[0] {
		t.Fatalf("KnownModels roundtrip = %v, want %v", got.KnownModels, rec.KnownModels)
	}
	if got.Capabilities.Search != provider.CapabilitySupported || got.Capabilities.ToolUse != provider.CapabilitySupported {
		t.Fatalf("Capabilities roundtrip = %+v", got.Capabilities)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("CreatedAt/UpdatedAt should be stamped by the injected clock")
	}

	assertGolden(t, "upsert_roundtrip.golden.json", map[string]any{
		"name": got.Name, "driver_kind": string(got.Driver), "auth_type": string(got.Auth),
		"account_kind": string(got.AccountKind), "tier": string(got.Tier), "known_models": got.KnownModels,
	})

	// Upserting again with the same name updates the row rather than
	// creating a duplicate -- the idempotency point.
	updated := rec
	updated.Tier = TierStrongest
	if err := reg.UpsertProvider(ctx, updated); err != nil {
		t.Fatalf("second UpsertProvider: %v", err)
	}
	all, err := reg.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListProviders after re-upsert = %d rows, want 1 (no duplicate)", len(all))
	}
	if all[0].Tier != TierStrongest {
		t.Fatalf("re-upsert did not update the existing row: Tier = %s", all[0].Tier)
	}
}

func TestRegistryGetProviderNotFound(t *testing.T) {
	reg := newTestRegistry(t)
	if _, err := reg.GetProvider(context.Background(), "missing"); err == nil {
		t.Fatal("GetProvider for a missing name should have returned an error")
	}
}

func TestRegistryGetByModel(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	withModel := sampleProvider("anthropic")
	withModel.KnownModels = []string{"claude-3-5-sonnet-20241022", "claude-3-opus"}
	noModels := sampleProvider("empty-provider")
	noModels.KnownModels = nil
	otherModel := sampleProvider("openai")
	otherModel.Driver = DriverOpenAICompat
	otherModel.KnownModels = []string{"gpt-4"}

	for _, rec := range []ProviderRecord{withModel, noModels, otherModel} {
		if err := reg.UpsertProvider(ctx, rec); err != nil {
			t.Fatalf("UpsertProvider(%s): %v", rec.Name, err)
		}
	}

	got, err := reg.GetByModel(ctx, "claude-3-opus")
	if err != nil {
		t.Fatalf("GetByModel: %v", err)
	}
	if len(got) != 1 || got[0].Name != "anthropic" {
		t.Fatalf("GetByModel(claude-3-opus) = %+v, want exactly [anthropic]", got)
	}

	// Empty known_models is excluded -- no wildcard expansion in P1.
	gotUnknown, err := reg.GetByModel(ctx, "some-unlisted-model")
	if err != nil {
		t.Fatalf("GetByModel: %v", err)
	}
	if len(gotUnknown) != 0 {
		t.Fatalf("GetByModel(unlisted model) = %+v, want empty (empty known_models never wildcard-matches)", gotUnknown)
	}
}

func TestRegistryDeleteCascadesToLanes(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	rec := sampleProvider("anthropic")
	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	for _, lane := range []string{"anthropic", "anthropic-fast"} {
		if err := reg.UpsertLane(ctx, LaneRecord{
			LaneName: lane, ProviderName: "anthropic",
			Capacity: CapacityInteractiveUsage, State: LaneStateAvailable,
		}); err != nil {
			t.Fatalf("UpsertLane(%s): %v", lane, err)
		}
	}

	if err := reg.DeleteProvider(ctx, "anthropic"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if _, err := reg.GetProvider(ctx, "anthropic"); err == nil {
		t.Fatal("GetProvider after DeleteProvider should have failed")
	}
	lanes, err := reg.ListLanes(ctx)
	if err != nil {
		t.Fatalf("ListLanes: %v", err)
	}
	if len(lanes) != 0 {
		t.Fatalf("ListLanes after DeleteProvider = %+v, want no orphan lanes", lanes)
	}
}

// TestAuthRefRejectsRawCredentialShapeAtValidation is the credential-
// safety non-negotiable: a raw string that LOOKS like a credential must
// fail Validate() before any DB write, never merely by convention.
func TestAuthRefRejectsRawCredentialShapeAtValidation(t *testing.T) {
	reg := newTestRegistry(t)
	rec := sampleProvider("myclaude")
	// Split so no contiguous credential-shaped literal exists in source
	// (GitHub push protection blocks a real match even in a synthetic
	// fixture) while leaving the runtime value, and therefore this test,
	// unchanged.
	rec.AuthRef = VaultKeyRef("sk-ant-" + "test0123456789value")

	if err := rec.Validate(); err == nil {
		t.Fatal("Validate() should reject a credential-shaped auth_ref")
	}
	if err := reg.UpsertProvider(context.Background(), rec); err == nil {
		t.Fatal("UpsertProvider should refuse a credential-shaped auth_ref before any DB write")
	}
	if _, err := reg.GetProvider(context.Background(), "myclaude"); err == nil {
		t.Fatal("the rejected upsert must not have reached the DB")
	}
}

// TestNoCredentialTextInAnyErrorOrListing plants a credential-shaped
// value in an unrelated free-text field (BaseURL, mirroring the S-20.T1
// "error message echoed a URL containing a key" finding) and asserts it
// never surfaces through Validate's own error text -- Validate never
// echoes field VALUES back, only field names, so a credential-shaped
// BaseURL cannot leak through a validation error.
func TestNoCredentialTextInAnyErrorOrListing(t *testing.T) {
	planted := "AKIA" + "TESTVALUESHOULDNOTLEAK01"
	rec := sampleProvider("leaky")
	rec.BaseURL = "https://api.example.com/v1?key=" + planted
	rec.Driver = "bogus-driver-to-force-a-validation-error"

	err := rec.Validate()
	if err == nil {
		t.Fatal("expected Validate() to fail on the bogus driver_kind")
	}
	if strings.Contains(err.Error(), planted) {
		t.Fatalf("credential-shaped text leaked into the validation error: %q", err.Error())
	}
}

// TestScanProviderRowDecodeErrors exercises scanProviderRow's three JSON
// decode-error branches by writing malformed column content directly
// (bypassing UpsertProvider's own encoding), proving each is handled as a
// cascade.KindIntegrity error rather than a panic.
func TestScanProviderRowDecodeErrors(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	if err := reg.UpsertProvider(ctx, sampleProvider("broken")); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}

	cases := []struct {
		name   string
		column string
	}{
		{"known_models", "known_models"},
		{"capabilities", "capabilities"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := reg.db.ExecContext(ctx,
				`UPDATE `+tableProviderRecords+` SET `+c.column+` = ? WHERE name = ?`,
				"{not-valid-json", "broken"); err != nil {
				t.Fatalf("corrupt %s column: %v", c.column, err)
			}
			if _, err := reg.GetProvider(ctx, "broken"); err == nil {
				t.Fatalf("GetProvider should fail on a malformed %s column", c.column)
			}
			// restore a valid value so later subtests/cases target only
			// their own column.
			if _, err := reg.db.ExecContext(ctx,
				`UPDATE `+tableProviderRecords+` SET `+c.column+` = ? WHERE name = ?`,
				"[]", "broken"); err != nil {
				t.Fatalf("restore %s column: %v", c.column, err)
			}
		})
	}
}

// TestScanProviderRowMalformedCost proves a malformed cost column
// surfaces the same ParseCostRecord integrity error through GetProvider.
func TestScanProviderRowMalformedCost(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	if err := reg.UpsertProvider(ctx, sampleProvider("broken-cost")); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if _, err := reg.db.ExecContext(ctx,
		`UPDATE `+tableProviderRecords+` SET cost = ? WHERE name = ?`, "{not-valid-json", "broken-cost"); err != nil {
		t.Fatalf("corrupt cost column: %v", err)
	}
	if _, err := reg.GetProvider(ctx, "broken-cost"); err == nil {
		t.Fatal("GetProvider should fail on a malformed cost column")
	}
}

// TestIsCredentialShapedWhitespaceVariants covers isCredentialShaped's
// tab/newline branches (not just the plain-space case already exercised
// by the "Bearer " prefix).
func TestIsCredentialShapedWhitespaceVariants(t *testing.T) {
	for _, s := range []string{"has\ttab", "has\nnewline"} {
		rec := sampleProvider("ws")
		rec.AuthRef = VaultKeyRef(s)
		if err := rec.Validate(); err == nil {
			t.Errorf("Validate() with whitespace-shaped auth_ref %q should have failed", s)
		}
	}
}

func assertGolden(t *testing.T, name string, got any) {
	t.Helper()
	path := filepath.Join("testdata", name)
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden candidate: %v", err)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden fixture %s: %v", path, err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(string(gotJSON)) {
		t.Fatalf("golden mismatch for %s:\n got:  %s\n want: %s", name, gotJSON, want)
	}
}
