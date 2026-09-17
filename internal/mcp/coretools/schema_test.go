package coretools_test

// Purpose: the schema half of P1-E16-W4-S34-T2's checks — every registered
//   tool's JSON Schema conforms to the subset MCP's own wire uses and
//   survives the tools/list encoding unchanged.
// Constraints: split from tools_test.go to stay under Art.10.3's 300-line
//   cap. The shape asserted here is the one the captured exchange under
//   internal/mcp/testdata/goldens shows a real first-party client
//   receiving and accepting — not a shape this repository invented.

import (
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/mcp/coretools"
)

// TestMCPToolSchemaRoundTrip asserts every registered schema conforms to
// the JSON Schema subset MCP's own wire uses — the shape the captured
// captured exchange in internal/mcp/testdata/goldens shows a real
// first-party client receiving and accepting — and survives the
// tools/list encoding verbatim.
func TestMCPToolSchemaRoundTrip(t *testing.T) {
	for _, spec := range coretools.Specs() {
		t.Run(spec.Name, func(t *testing.T) {
			assertMCPSchema(t, spec.Schema)
			encoded, err := json.Marshal(spec.Schema)
			if err != nil {
				t.Fatalf("schema does not marshal: %v", err)
			}
			var back map[string]any
			if err := json.Unmarshal(encoded, &back); err != nil {
				t.Fatalf("schema does not round-trip: %v", err)
			}
			if len(back) != len(spec.Schema) {
				t.Fatalf("round-trip changed the schema's key count: %d -> %d", len(spec.Schema), len(back))
			}
		})
	}
}

// assertMCPSchema checks the subset rules: an object schema, with a
// properties map, an array of required names that all exist as
// properties, and every property carrying a type and a description.
func assertMCPSchema(t *testing.T, schema map[string]any) {
	t.Helper()
	if schema["type"] != "object" {
		t.Fatalf(`schema type = %v, want "object" — MCP tool arguments are always an object`, schema["type"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema declares no properties map")
	}
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("property %q is not an object", name)
		}
		if prop["type"] == nil {
			t.Errorf("property %q declares no type", name)
		}
		if prop["description"] == nil {
			t.Errorf("property %q has no description; a model must guess what it means", name)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("schema required = %T, want []string", schema["required"])
	}
	for _, name := range required {
		if _, exists := props[name]; !exists {
			t.Errorf("required names %q, which is not a property", name)
		}
	}
	if schema["additionalProperties"] != false {
		t.Error("schema permits additional properties; a tool that silently accepts invented arguments teaches a model they were fine")
	}
}
