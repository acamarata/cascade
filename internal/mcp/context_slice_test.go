package mcp

// Purpose: documents and asserts the current state of the kept v1-parity
//   MCP alias cascade_context_slice this ticket's contract names
//   ("cascade_context_slice -> context.slice", 07-CLI-COMMAND-TREE §mirror
//   rule).
//
// Contract/tree note (see this ticket's journal for the full quoted
// contradiction): exactly the same gap internal/mcp/context_scope_test.go
// already pins for cascade_context_scope_show applies here. A builtin's MCP
// exposure is compile-time-registered via pkg/plugin.RegisterBuiltin,
// called from a builtin plugin package's own init() -- a package the
// binary composition root then blank-imports (pkg/plugin/register.go's own
// doc comment). The composition root that would perform that blank-import
// is cmd/cascade's plugin-registration file, which is outside this
// ticket's files_scope (this ticket's files_scope names
// internal/rpc/registry.go, internal/mcp/registry.go, cmd/cascade/root.go
// and internal/daemon/*.go -- not a plugin package or its blank-import
// site). No production internal/context/scope- or internal/context-backed
// builtin plugin exists anywhere in the tree today (grep of
// pkg/plugin.RegisterBuiltin callers finds only the example builtin under
// plugins/examples). Registering one here, with no legal in-scope caller
// to blank-import it, would itself be exactly the R-14.166/171/175
// "built, tested, called by nothing that ships" pattern this phase's
// rulings forbid -- so this ticket does not add that registration. This
// test instead pins the CURRENT, honest state: no cascade_context_slice
// tool exists in the exposed set today, so a later ticket that DOES wire
// it (with an in-scope blank-import site) has a red-then-green signal to
// work against, matching this ticket's Article-1 anti-stub discipline.
import (
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

func TestContextSliceToolNotYetExposed(t *testing.T) {
	registry := NewToolRegistry(plugin.Builtins)
	for _, tool := range registry.List() {
		if tool.Name == "cascade_context_slice" {
			t.Fatalf("cascade_context_slice is exposed, but no in-scope composition-root wiring registered it in this ticket -- update this test once a later ticket adds the blank-import site")
		}
	}
}
