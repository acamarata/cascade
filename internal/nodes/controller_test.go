package nodes

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type fakeControllerBindingBackend struct {
	binding ControllerBinding
	found   bool
	err     error
}

func (b fakeControllerBindingBackend) Load() (ControllerBinding, bool, error) {
	return b.binding, b.found, b.err
}

func TestResolveRoleNodeWhenBindingFound(t *testing.T) {
	role, err := ResolveRole(fakeControllerBindingBackend{binding: ControllerBinding{Endpoint: "x"}, found: true})
	if err != nil {
		t.Fatal(err)
	}
	if role != RoleNode {
		t.Fatalf("role = %q, want node", role)
	}
}

func TestResolveRoleControllerWhenNoBinding(t *testing.T) {
	role, err := ResolveRole(fakeControllerBindingBackend{found: false})
	if err != nil {
		t.Fatal(err)
	}
	if role != RoleController {
		t.Fatalf("role = %q, want controller", role)
	}
}

func TestResolveRoleControllerWhenBackendNil(t *testing.T) {
	role, err := ResolveRole(nil)
	if err != nil {
		t.Fatal(err)
	}
	if role != RoleController {
		t.Fatalf("role = %q, want controller for a nil backend", role)
	}
}

func TestResolveRolePropagatesLoadError(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "boom")
	_, err := ResolveRole(fakeControllerBindingBackend{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestNodeRoleRefusesSchedulerRPCs(t *testing.T) {
	for _, method := range GuardedMethods {
		err := RequireController(RoleNode, method)
		if err == nil {
			t.Fatalf("RequireController(node, %q) = nil, want ErrNotController", method)
		}
		var kerr *cascade.Error
		if !errors.As(err, &kerr) || kerr.Kind != cascade.KindPermissionDenied {
			t.Fatalf("RequireController(node, %q) err = %v, want KindPermissionDenied", method, err)
		}
	}
}

func TestControllerRoleAllowsSchedulerRPCs(t *testing.T) {
	for _, method := range GuardedMethods {
		if err := RequireController(RoleController, method); err != nil {
			t.Fatalf("RequireController(controller, %q) = %v, want nil", method, err)
		}
	}
}

func TestRoleFromContextDefaultsToNode(t *testing.T) {
	if got := RoleFromContext(context.Background()); got != RoleNode {
		t.Fatalf("RoleFromContext(bare ctx) = %q, want node (fail-closed)", got)
	}
}

func TestRoleFromContextRoundTrip(t *testing.T) {
	ctx := WithRole(context.Background(), RoleController)
	if got := RoleFromContext(ctx); got != RoleController {
		t.Fatalf("RoleFromContext(WithRole(controller)) = %q, want controller", got)
	}
}
