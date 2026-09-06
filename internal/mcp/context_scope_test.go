package mcp

// Purpose: documents and asserts the current state of the full-profile MCP
//   tool cascade_context_scope_show this ticket's contract names.
//
// Contract/tree note (see this ticket's journal for the full quoted
// contradiction): a builtin's MCP exposure is compile-time-registered via
// pkg/plugin.RegisterBuiltin, called from a builtin plugin package's own
// init() -- a package the binary composition root then blank-imports (see
// pkg/plugin/register.go's own doc comment: "a compile-time blank-import
// of the plugin package ... is sufficient to make the plugin known to the
// host"). The composition root that performs this blank-import is
// cmd/cascade's plugin-registration file, which is outside this ticket's
// files_scope (not internal/rpc/registry.go, internal/mcp/registry.go,
// internal/daemon/daemon.go, or cmd/cascade/root.go -- the four files this
// ticket is scoped to change). Registering internal/context/scope as a
// pkg/plugin.BuiltinRegistration without a legal in-scope caller to
// blank-import it would itself be exactly the R-14.166/171/175 "built,
// tested, called by nothing that ships" pattern this phase's rulings
// forbid -- so this ticket does not add that registration. This test
// instead pins the CURRENT, honest state: no cascade_context_scope_show
// tool exists in the exposed set today, so a later ticket that DOES wire
// it (with an in-scope blank-import site) has a red-then-green signal to
// work against, matching this ticket's Article-1 anti-stub discipline.
import (
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

func TestContextScopeShowToolNotYetExposed(t *testing.T) {
	registry := NewToolRegistry(plugin.Builtins)
	for _, tool := range registry.List() {
		if tool.Name == "cascade_context_scope_show" {
			t.Fatalf("cascade_context_scope_show is exposed, but no in-scope composition-root wiring registered it in this ticket -- update this test once a later ticket adds the blank-import site")
		}
	}
}
