package runtime

import (
	"errors"
	"strings"
	"testing"
)

func TestUpgradeSchema_V0ToCurrent(t *testing.T) {
	tree := map[string]interface{}{
		"runtime": map[string]interface{}{"profile": "local"},
	}
	result, err := UpgradeSchema(tree)
	if err != nil {
		t.Fatalf("UpgradeSchema: %v", err)
	}
	if result.FromVersion != 0 {
		t.Errorf("FromVersion = %d, want 0", result.FromVersion)
	}
	if result.ToVersion != CurrentSchemaVersion {
		t.Errorf("ToVersion = %d, want %d", result.ToVersion, CurrentSchemaVersion)
	}
	if !result.Mutated {
		t.Error("Mutated = false, want true")
	}
	if got := schemaVersionOf(tree); got != CurrentSchemaVersion {
		t.Errorf("tree schema_version = %d, want %d", got, CurrentSchemaVersion)
	}
	// The rest of the tree must survive the upgrade untouched.
	rt, ok := tree["runtime"].(map[string]interface{})
	if !ok || rt["profile"] != "local" {
		t.Errorf("runtime section not preserved across upgrade: %#v", tree["runtime"])
	}
}

func TestUpgradeSchema_IdempotentOnCurrent(t *testing.T) {
	tree := map[string]interface{}{"schema_version": int64(CurrentSchemaVersion)}
	first, err := UpgradeSchema(tree)
	if err != nil {
		t.Fatalf("UpgradeSchema (first run): %v", err)
	}
	// The tree was seeded already at CurrentSchemaVersion, so the first
	// run itself must be a no-op too.
	if first.Mutated {
		t.Error("first run against an already-current tree: Mutated = true, want false")
	}
	second, err := UpgradeSchema(tree)
	if err != nil {
		t.Fatalf("UpgradeSchema (second run): %v", err)
	}
	if second.Mutated {
		t.Error("second run against an already-current tree: Mutated = true, want false")
	}
	if second.FromVersion != CurrentSchemaVersion || second.ToVersion != CurrentSchemaVersion {
		t.Errorf("second run version = %d -> %d, want %d -> %d", second.FromVersion, second.ToVersion, CurrentSchemaVersion, CurrentSchemaVersion)
	}
}

func TestUpgradeSchema_DoubleRunIsIdempotent(t *testing.T) {
	tree := map[string]interface{}{
		"runtime": map[string]interface{}{"profile": "server"},
		"logging": map[string]interface{}{"level": "info"},
	}
	if _, err := UpgradeSchema(tree); err != nil {
		t.Fatalf("first UpgradeSchema: %v", err)
	}
	second, err := UpgradeSchema(tree)
	if err != nil {
		t.Fatalf("second UpgradeSchema: %v", err)
	}
	if second.Mutated {
		t.Error("second run: Mutated = true, want false (idempotency violated)")
	}
}

func TestUpgradeSchema_NewerThanBinaryIsHardError(t *testing.T) {
	tree := map[string]interface{}{"schema_version": int64(CurrentSchemaVersion + 1)}
	_, err := UpgradeSchema(tree)
	if err == nil {
		t.Fatal("expected an error for a future schema_version, got nil")
	}
	var se *SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want *SchemaError", err)
	}
	if se.FoundVersion != CurrentSchemaVersion+1 || se.CurrentVersion != CurrentSchemaVersion {
		t.Errorf("SchemaError = %+v", se)
	}
}

func TestSchemaError_Error(t *testing.T) {
	err := &SchemaError{FoundVersion: 5, CurrentVersion: 1}
	msg := err.Error()
	if !strings.Contains(msg, "5") || !strings.Contains(msg, "1") {
		t.Errorf("Error() = %q, want it to mention both versions", msg)
	}
}

func TestSchemaVersionOf_TypeTolerance(t *testing.T) {
	tests := []struct {
		name string
		tree map[string]interface{}
		want int
	}{
		{"absent", map[string]interface{}{}, 0},
		{"int64", map[string]interface{}{"schema_version": int64(3)}, 3},
		{"int", map[string]interface{}{"schema_version": 3}, 3},
		{"float64", map[string]interface{}{"schema_version": float64(3)}, 3},
		{"wrong type", map[string]interface{}{"schema_version": "three"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := schemaVersionOf(tc.tree); got != tc.want {
				t.Errorf("schemaVersionOf(%v) = %d, want %d", tc.tree, got, tc.want)
			}
		})
	}
}

// TestConfigGoldenSchemaUpgrade is the S-04.T1 golden-fixture test for the
// schema_version upgrade-rewrite frame (contract check: `go test
// ./internal/runtime/... -run TestConfigGolden -update`). It is named to
// match the shared -run TestConfigGolden pattern alongside config_test.go's
// golden tests.
func TestConfigGoldenSchemaUpgrade(t *testing.T) {
	tree := map[string]interface{}{
		"runtime": map[string]interface{}{"profile": "server"},
		"elevation": map[string]interface{}{
			"allow_remote":  false,
			"helper_pubkey": "",
		},
		"logging": map[string]interface{}{"level": "info"},
	}
	result, err := UpgradeSchema(tree)
	if err != nil {
		t.Fatalf("UpgradeSchema: %v", err)
	}
	got := renderSchemaUpgradeGolden(result, tree)
	compareGolden(t, "schema_upgrade_v0_to_current.golden", got)

	// Re-running must be a no-op (idempotency, asserted golden-side too).
	second, err := UpgradeSchema(tree)
	if err != nil {
		t.Fatalf("UpgradeSchema (rerun): %v", err)
	}
	if second.Mutated {
		t.Error("rerun against the upgraded tree: Mutated = true, want false")
	}
}
