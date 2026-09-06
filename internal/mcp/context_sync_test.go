package mcp

// Purpose: documents and asserts the current state of a would-be
//   cascade_context_sync MCP tool (E/S-09.T4). Mirrors
//   context_slice_test.go's own pin exactly, for the same reason: a
//   builtin's MCP exposure is compile-time-registered via
//   pkg/plugin.RegisterBuiltin, called from a builtin plugin package's own
//   init() -- a package the binary composition root then blank-imports
//   (pkg/plugin/register.go's own doc comment). The composition root that
//   would perform that blank-import is cmd/cascade's plugin-registration
//   file, which is outside this ticket's files_scope (this ticket's
//   files_scope names internal/rpc/registry.go and internal/mcp/registry.go
//   -- not a plugin package or its blank-import site, and neither of those
//   two files is a registration site either; see this ticket's journal for
//   the full contract/tree note, which is the SEVENTH ticket to hit exactly
//   this gap after E/S-09.T2's). No production internal/context-backed
//   builtin plugin exists anywhere in the tree today (grep of
//   pkg/plugin.RegisterBuiltin callers finds only the example builtin
//   under plugins/examples). Registering one here, with no legal in-scope
//   caller to blank-import it, would itself be exactly the R-14.166/171/175
//   "built, tested, called by nothing that ships" pattern this phase's
//   rulings forbid -- so this ticket does not add that registration. This
//   test instead pins the CURRENT, honest state: no cascade_context_sync
//   tool exists in the exposed set today, so a later ticket that DOES wire
//   it (with an in-scope blank-import site) has a red-then-green signal to
//   work against, matching this ticket's Article-1 anti-stub discipline.
import (
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

func TestContextSyncToolNotYetExposed(t *testing.T) {
	registry := NewToolRegistry(plugin.Builtins)
	for _, tool := range registry.List() {
		if tool.Name == "cascade_context_sync" {
			t.Fatalf("cascade_context_sync is exposed, but no in-scope composition-root wiring registered it in this ticket -- update this test once a later ticket adds the blank-import site")
		}
	}
}
