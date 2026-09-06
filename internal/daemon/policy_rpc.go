package daemon

// Purpose: registers the approval.* / standing_grant.* / policy.* method
//   set (I/S-18.T6) on the daemon's RPC router, following the same
//   per-ticket registration-file split context_scope.go already applies.
//
// Inputs: the daemon's shared *rpc.Registry and the handler map
//   internal/policy built over the process's one policy engine, approval
//   queue, grant store and audit log.
//
// Outputs: the approval and policy namespaces, reachable over the socket.
//
// Constraints: this file adapts and registers, and decides nothing. Every
//   authorization question was already answered inside the handler, which
//   runs policy.Authorize before it touches a collaborator; a second
//   check here would be a second policy, and two policies over one verb
//   is the drift R-21.207 forbids. An empty handler set is refused rather
//   than registered as an empty namespace, because a namespace that
//   resolves to nothing is indistinguishable at the far end from one that
//   was never wired.
//
// SPORT: internal/daemon (CHANGED, approval/policy registration,
//   P1-E09-W2-S18-T6).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RegisterPolicyHandlers binds every handler in handlers against registry.
// A nil registry or an empty handler set is refused.
func RegisterPolicyHandlers(registry *rpc.Registry, handlers map[string]policy.MethodFunc) error {
	if registry == nil {
		return cascade.New(cascade.KindInvalidInput,
			"daemon: registering the policy handlers requires a method registry")
	}
	if len(handlers) == 0 {
		return cascade.New(cascade.KindInvalidInput,
			"daemon: the policy handler set is empty, so there is nothing to register")
	}
	for method, handler := range handlers {
		registry.Register(method, adaptPolicyHandler(handler))
	}
	return nil
}

// adaptPolicyHandler converts a policy.MethodFunc into the registry's own
// handler type. The two are structurally identical; the conversion exists
// so internal/policy never imports the RPC server package.
func adaptPolicyHandler(handler policy.MethodFunc) rpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		return handler(ctx, params)
	}
}
