package client

// Purpose: the ContextScopeShow typed method wrapper over
//   context.scope.show (internal/daemon.ContextScopeMethod; see
//   internal/daemon/context_scope.go's RegisterContextScopeHandler, the
//   real handler cmd/cascade/daemon_unix_run.go's buildRPCServer
//   registers, and internal/context/scope/rpc.go's ContextScopeShow, the
//   decode+resolve function that handler and the CLI's embedded runtime
//   path both call). Follows status.go's exact established pattern:
//   cmd/cascade/context_scope.go calls this instead of assembling the
//   request/decoding the response itself (hard requirement 1).
import (
	"context"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
)

// ContextScopeShow calls context.scope.show and returns the resolved
// SessionScope.
func (c *Client) ContextScopeShow(ctx context.Context, params scope.ScopeShowParams) (scope.SessionScope, error) {
	var res scope.SessionScope
	if err := c.Do(ctx, daemon.ContextScopeMethod, params, &res); err != nil {
		return scope.SessionScope{}, err
	}
	return res, nil
}
