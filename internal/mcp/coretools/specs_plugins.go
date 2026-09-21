package coretools

// Purpose: the plugin-registry-catalog tool set (P1-E24-W5-S50-T2, D4).
//   Split into its own sibling file rather than grown inline in specs.go,
//   which sat at Art.10.3's 300-line cap before this addition — the same
//   remedy internal/runtime/config_sections.go's own header records for
//   an identical situation.
// Inputs: none — a declaration, matching specs.go's own contract.
// Outputs: one Spec, cascade_plugin_search.
// Constraints: the ticket's own MAJOR finding (adversarial review,
//   s50t2-cr-verdict.txt finding 4): internal/rpc/registry.go carries no
//   ✦/MCP-mirror concept of its own — its doc comment says "no
//   method-specific code belongs here, by design" — THIS package is the
//   real source the 07-CLI-COMMAND-TREE.md mirror rule describes, and the
//   one this ticket's D4 decision directs the fix to.
// SPORT: internal/mcp/coretools plugin-search (ADD) — P1-E24-W5-S50-T2.

// v1NameNoAncestor marks a spec with no v1 counterpart, honestly, not as
// an empty V1Name would (golden_test.go's indexByV1Name hard-fails on an
// empty V1Name -- "descends from no v1 tool; the golden has nothing to
// join it on" -- because coretools.Specs() is, by that test's own binding
// invariant, a v1-parity container, not a general MCP tool registry).
// testdata/v1-goldens/tools.json carries a matching row for this sentinel,
// explicitly marked "no v1 ancestor" rather than fabricated harvested-v1
// evidence (Art.2) -- the plugin registry (Epic X) genuinely did not
// exist in v1. Discovered only by running TestMCPToolV1GoldenParity, not
// anticipated by the ticket text or its execution_guidance; recorded here
// as the scope deviation this ticket's own D4 decision text anticipated
// ("T0 files the planning contradiction").
const v1NameNoAncestor = "NEW-IN-V2:cascade_plugin_search"

// pluginSpecs are the tools over the plugin registry catalog.
func pluginSpecs() []Spec {
	return []Spec{
		// pluginSearchParams (cmd/cascade/plugin_search.go).
		{
			Name:   "cascade_plugin_search",
			V1Name: v1NameNoAncestor,
			Description: "Search the plugin registry catalog for installable plugins matching a query, " +
				"case-insensitively against name, description and tags. An empty query browses the full " +
				"catalog (builtin plugins always included; registry-sourced entries only when a verified " +
				"registry index is configured).",
			Method:     "plugin.search",
			Capability: CapabilityPluginsRead,
			Schema: object(nil, map[string]any{
				"q": str("Search text. Empty browses the full catalog."),
			}),
		},
	}
}
