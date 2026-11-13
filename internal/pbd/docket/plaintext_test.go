package docket

import "testing"

// TestSchemaTextColumnsAreText verifies that SchemaColumnTypes returns exactly
// the title, objective, acceptance, and verify columns mapped to TEXT, and
// that each call returns a fresh map that is independent of earlier results.
func TestSchemaTextColumnsAreText(t *testing.T) {
	want := map[string]string{
		"title":      "TEXT",
		"objective":  "TEXT",
		"acceptance": "TEXT",
		"verify":     "TEXT",
	}

	got := SchemaColumnTypes()
	if len(got) != len(want) {
		t.Fatalf("SchemaColumnTypes returned %d entries, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("SchemaColumnTypes()[%q] = %q, want %q", k, got[k], v)
		}
	}

	got["title"] = "MUTATED"
	got["injected"] = "TEXT"
	delete(got, "objective")

	fresh := SchemaColumnTypes()
	if len(fresh) != len(want) {
		t.Fatalf("fresh SchemaColumnTypes returned %d entries, want %d", len(fresh), len(want))
	}
	for k, v := range want {
		if fresh[k] != v {
			t.Errorf("fresh SchemaColumnTypes()[%q] = %q, want %q", k, fresh[k], v)
		}
	}
	if _, ok := fresh["injected"]; ok {
		t.Error("fresh SchemaColumnTypes contains unexpected key injected")
	}
}
