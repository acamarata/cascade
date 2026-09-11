// Purpose: R-21.169's controller-singleton guard: Role() resolves whether
//	this daemon is the controller or a node, and RequireController
//	refuses job.advance/lease.acquire/lease.release when it is not.
// Inputs: a ControllerBindingBackend (serve.go, S-36.T2) — the real,
//	already-persisted "enrollment record" this ticket's contract names:
//	its presence means this daemon enrolled ITSELF with a controller
//	(the node role); its absence means this daemon has never enrolled
//	anywhere, i.e. it is the controller.
// Outputs: a Role, or a typed cascade.KindPermissionDenied error from
//	RequireController.
// Constraints: R-21.169 — the DAG scheduler and lease authority run ONLY
//	on the controller daemon under the existing DB advisory lock; a node
//	daemon refuses job.advance/lease.acquire/lease.release with
//	ErrNotController rather than silently executing them locally. This
//	file exposes the guard primitive only; AC/S-59.T5's own handlers
//	apply it (forward note per this ticket's full_desc item 9 — that
//	file change belongs to AC/S-59.T5, not here).
// SPORT: internal/nodes Role/ADDED, RequireController/ADDED,
//	ErrNotController/ADDED (P1-E36-W7-S72-T2).

package nodes

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Role is a daemon's resolved position in the controller-singleton
// scheme (R-21.169).
type Role string

const (
	// RoleController runs the DAG scheduler and the lease authority.
	RoleController Role = "controller"
	// RoleNode is a fleet member enrolled with a controller; it refuses
	// scheduler/lease RPCs rather than acting as its own scheduler.
	RoleNode Role = "node"
)

// GuardedMethods names the JSON-RPC methods RequireController refuses on
// a node daemon (R-21.169's three named RPCs).
var GuardedMethods = []string{"job.advance", "lease.acquire", "lease.release"}

// ResolveRole resolves this daemon's Role from binding: a successfully
// loaded ControllerBinding means this daemon has enrolled itself with a
// controller (RoleNode); no binding found means this daemon is the
// controller (RoleController). binding is nil-safe: a nil
// ControllerBindingBackend resolves to RoleController, matching an
// embedded/daemonless or not-yet-configured mode's safe default (no
// controller to defer to, so this instance IS the authority, exactly the
// posture a single-node dev setup needs).
func ResolveRole(binding ControllerBindingBackend) (Role, error) {
	if binding == nil {
		return RoleController, nil
	}
	_, ok, err := binding.Load()
	if err != nil {
		return "", err
	}
	if ok {
		return RoleNode, nil
	}
	return RoleController, nil
}

// ErrNotController reports that method is refused because this daemon's
// Role is not RoleController — R-21.169's fail-closed guard. KindPermissionDenied:
// the caller is not entitled to invoke a controller-only RPC from a node
// daemon, regardless of any per-request auth that already passed.
func ErrNotController(method string) error {
	return cascade.Newf(cascade.KindPermissionDenied,
		"nodes: %q is refused on a node daemon: the DAG scheduler and lease authority run only on the controller (R-21.169)", method)
}

// RequireController returns ErrNotController(method) unless role is
// RoleController. Callers (AC/S-59.T5's job.advance/lease.acquire/
// lease.release handlers) call this before doing any work — the guard
// runs before, not instead of, the handler's own logic.
func RequireController(role Role, method string) error {
	if role != RoleController {
		return ErrNotController(method)
	}
	return nil
}

// requireControllerCtx is an alternate call shape some future handler
// wiring may prefer — role carried on the context rather than passed
// explicitly. Not used by this ticket's own tests beyond proving the
// round-trip; AC/S-59.T5 chooses whichever shape its own handlers need.
type roleContextKey struct{}

// WithRole returns a context carrying role, for a handler chain that
// resolves Role once at connection time.
func WithRole(ctx context.Context, role Role) context.Context {
	return context.WithValue(ctx, roleContextKey{}, role)
}

// RoleFromContext reads the Role WithRole stored, defaulting to RoleNode
// (the more restrictive role) when ctx carries none — fail-closed: an
// un-decorated context must never be silently treated as the
// controller.
func RoleFromContext(ctx context.Context) Role {
	if role, ok := ctx.Value(roleContextKey{}).(Role); ok {
		return role
	}
	return RoleNode
}
