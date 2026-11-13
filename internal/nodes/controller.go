// Purpose: resolve explicit controller authority and guard controller RPCs.
// Inputs: persisted designation and controller binding in the same data directory.
// Outputs: a role or a typed integrity conflict; permission errors for node RPCs.
// Constraints: no implicit authority, election or designation writes.
// SPORT: internal/nodes controller-designation (P1-NODE-16).

package nodes

import (
	"context"
	"reflect"
	"strings"

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

// RoleConflict describes why persisted records cannot establish a role.
type RoleConflict struct {
	Kind   string // none | both | binding-incomplete | load-error | no-source
	Detail string
}

// ResolveRole requires a readable designation for controller authority, or a
// complete binding alone for a node. Every ambiguous state fails closed.
func ResolveRole(binding ControllerBindingBackend) (Role, error) {
	role, conflict, cause := resolveRoleStatus(binding)
	if conflict != nil {
		err := roleConflictError(*conflict)
		if cause != nil {
			return "", cascade.Wrap(cascade.KindIntegrity, cause, err.Error())
		}
		return "", err
	}
	return role, nil
}

// RoleStatus returns a diagnostic conflict instead of granting ambiguous
// authority. Backend failures, including panics, never escape this doctor view.
func RoleStatus(binding ControllerBindingBackend) (Role, *RoleConflict) {
	role, conflict, _ := resolveRoleStatus(binding)
	return role, conflict
}

func resolveRoleStatus(binding ControllerBindingBackend) (role Role, conflict *RoleConflict, cause error) {
	defer func() {
		if recover() != nil {
			role, conflict, cause = "", &RoleConflict{"load-error", "role backend panicked"}, nil
		}
	}()
	source, ok := binding.(DesignationSource)
	if nilRoleBackend(binding) || !ok {
		return "", &RoleConflict{"no-source", "binding must expose its persisted designation"}, nil
	}
	d := source.DesignationBackend()
	if nilRoleBackend(d) {
		return "", &RoleConflict{"no-source", "designation backend is required"}, nil
	}
	stored, designated, err := d.Load()
	if err != nil {
		return "", &RoleConflict{"load-error", "cannot read designation"}, err
	}
	if designated {
		if err := validateDesignation(stored); err != nil {
			return "", &RoleConflict{"load-error", "invalid stored designation"}, err
		}
	}
	b, bound, err := binding.Load()
	if err != nil {
		return "", &RoleConflict{"load-error", "cannot read binding"}, err
	}
	if bound && (strings.TrimSpace(b.Endpoint) == "" || strings.TrimSpace(b.ControllerPubKeyB64) == "") {
		return "", &RoleConflict{"binding-incomplete", "binding requires endpoint and pinned controller key"}, nil
	}
	if designated && bound {
		return "", &RoleConflict{"both", "designation and binding both exist"}, nil
	}
	if designated {
		return RoleController, nil, nil
	}
	if bound {
		return RoleNode, nil, nil
	}
	return "", &RoleConflict{"none", "no designation or complete binding exists"}, nil
}

func nilRoleBackend(backend any) bool {
	if backend == nil {
		return true
	}
	v := reflect.ValueOf(backend)
	k := v.Kind()
	nilable := k == reflect.Chan || k == reflect.Func || k == reflect.Interface ||
		k == reflect.Map || k == reflect.Pointer || k == reflect.Slice
	return nilable && v.IsNil()
}

func roleConflictError(c RoleConflict) error {
	return cascade.Wrap(cascade.KindIntegrity, ErrRoleConflict,
		"nodes: role conflict "+c.Kind+": "+c.Detail+"; run `cascade init` on a controller or `cascade node bind` on a node")
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
