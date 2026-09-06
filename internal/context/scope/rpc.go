package scope

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: ContextScopeShow is the wire-level entry point `cascade context
//   scope show` routes through directly (the embedded runtime), and the
//   shape internal/client.Client.ContextScopeShow decodes a daemon's
//   context.scope.show response into: decode the caller-supplied fields,
//   delegate to ResolveSessionScope, and return the resulting SessionScope
//   (or a *cascade.Error) in the D/S-06.T5 output-contract shape. The
//   daemon-side RPC registration (internal/daemon.RegisterContextScopeHandler,
//   called from cmd/cascade/daemon_unix_run.go's buildRPCServer) IS wired to
//   the real daemon composition root and proven reachable by
//   internal/integration.TestContextScopeRealCounterparts_WiringProof. See
//   this ticket's journal for why the full-profile MCP tool
//   cascade_context_scope_show is NOT wired to a live composition root by
//   this ticket (it needs a builtin-plugin blank-import site outside
//   files_scope): ContextScopeShow itself is the one decode+resolve path
//   that later blank-import site will call, so it cannot drift on field
//   names or error mapping once it lands.
// Inputs: raw JSON params (possibly nil/empty -- an absent params object
//   resolves against zero-value caller fields, matching an interactive
//   `cascade context scope show` with no flags) and ResolveDeps.
// Outputs: a SessionScope value, or an A-T7 typed error for malformed
//   params (never a panic, never a silently-empty success).
// Constraints: this file adds no capability, no addressing surface, and
//   no mutation grammar -- read-only, exactly ResolveSessionScope's own
//   contract.
// SPORT: cli/context-scope-show + rpc/context.scope.show +
//   mcp/cascade_context_scope_show/ADD.

// ShowParams is context.scope.show's wire params shape and
// `cascade context scope show`'s flag-derived input: every field
// ResolveInput accepts, JSON-tagged for the RPC/MCP transports and reused
// directly by the CLI and client layers so all three speak one shape.
type ShowParams struct {
	Cwd               string `json:"cwd,omitempty"`
	User              string `json:"user,omitempty"`
	Machine           string `json:"machine,omitempty"`
	Branch            string `json:"branch,omitempty"`
	Task              string `json:"task,omitempty"`
	Session           string `json:"session,omitempty"`
	ExplicitOverrides string `json:"explicit_overrides,omitempty"`
}

// ContextScopeShow decodes raw (a ShowParams JSON object, or empty)
// and resolves the SessionScope it describes. A malformed raw payload is
// an A-T7 KindInvalidInput error, decoded BEFORE any store access -- the
// fail-closed pattern this ticket's contract requires for "malformed
// params" test coverage.
func ContextScopeShow(ctx context.Context, deps ResolveDeps, raw json.RawMessage) (SessionScope, error) {
	var p ShowParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return SessionScope{}, cascade.Wrap(cascade.KindInvalidInput, err, "context/scope: malformed context.scope.show params")
		}
	}
	return ResolveSessionScope(ctx, deps, ResolveInput(p))
}
