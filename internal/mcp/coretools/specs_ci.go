package coretools

// Purpose: the local CI gate tool set -- cascade_ci_run runs the gate for
//   the MCP server's working directory, cascade_ci_status reads recorded
//   results. Split into its own sibling file because specs.go sits near
//   Art.10.3's 300-line cap.
// Inputs: none -- a declaration, matching specs.go's own contract.
// Outputs: two Specs, served by internal/ci's RegisterMCPMethods.
// Constraints: neither schema has a repository, path or command property
//   and both refuse extra properties: the gate runs where the server was
//   started and only runs the steps the operator configured under
//   [ci.local]. Neither result carries step output.

const (
	// CapabilityCIRun covers running the local gate: it executes the
	// operator's configured lint, test and build commands on this machine.
	CapabilityCIRun = "ci.run"
	// CapabilityCIRead covers reading recorded CI results.
	CapabilityCIRead = "ci.read"
)

// ciSpecs are the tools over the local CI gate. Both descend from no v1
// tool (v1 had no CI surface), so each carries the explicit no-ancestor
// sentinel and a matching row in testdata/v1-goldens/tools.json.
func ciSpecs() []Spec {
	return []Spec{
		// runMethodParams (internal/ci/mcp_methods.go).
		{
			Name:   "cascade_ci_run",
			V1Name: "NEW-IN-V2:cascade_ci_run",
			Description: "Run the local lint, test and build gate for the repository this server was " +
				"started in and record the result. Runs only the commands configured under [ci.local]. " +
				"Returns the run id, whether it passed, the failed step and each step's exit code; " +
				"step output is never returned. Refused for a repository whose CI belongs on hosted GitHub Actions.",
			Method:     "ci.run",
			Capability: CapabilityCIRun,
			Mutating:   true,
			Schema: object(nil, map[string]any{
				"lint_only":  boolean("Run only the lint step."),
				"test_only":  boolean("Run only the test step."),
				"build_only": boolean("Run only the build step."),
			}),
		},
		// statusMethodParams (internal/ci/mcp_methods.go).
		{
			Name:   "cascade_ci_status",
			V1Name: "NEW-IN-V2:cascade_ci_status",
			Description: "List recent CI results, from both GitHub Actions and the local gate, newest first. " +
				"Read-only.",
			Method:     "ci.status",
			Capability: CapabilityCIRead,
			Schema: object(nil, map[string]any{
				"limit": num("Maximum runs to return, 1 to 50. Defaults to 10."),
			}),
		},
	}
}
