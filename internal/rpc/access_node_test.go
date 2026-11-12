package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = DeclareAccess("test.node.owner.refuse", AccessNode)
var _ = DeclareAccess("test.unknown.refuse", AccessNode+1)
var _ = DeclareAccess("test.node.max.refuse", Access(255))

// TestOwnerRegistryRefusesAccessNode checks node and unknown classes fail closed.
func TestOwnerRegistryRefusesAccessNode(t *testing.T) {
	r := NewRegistry()
	r.SetAccessPolicy(nil)
	r.Use(func(_ string, next HandlerFunc) HandlerFunc {
		t.Error("refused call reached middleware")
		return next
	})
	cases := []struct {
		method string
		class  Access
		want   error
		kind   cascade.Kind
		code   int
		msg    string
	}{
		{"test.node.owner.refuse", AccessNode, errAccessDenied, cascade.KindPermissionDenied, cascade.RPCCodePermissionDenied, "caller not permitted"},
		{"test.unknown.refuse", AccessNode + 1, errUnknownAccess, cascade.KindInternal, cascade.RPCCodeInternal, "unknown access class"},
		{"test.node.max.refuse", Access(255), errUnknownAccess, cascade.KindInternal, cascade.RPCCodeInternal, "unknown access class"},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			if class, ok := AccessOf(tc.method); !ok || class != tc.class {
				t.Fatalf("AccessOf(%q) = (%d, %t), want (%d, true)", tc.method, class, ok, tc.class)
			}
			r.Register(tc.method, func(context.Context, json.RawMessage) (any, error) {
				t.Error("refused call reached handler")
				return nil, nil
			})
			for name, ctx := range ownerNodeRefusalCallers() {
				t.Run(name, func(t *testing.T) {
					assertOwnerNodeRefusal(ctx, t, r, tc.method, tc.want, tc.kind, tc.code, tc.msg)
				})
			}
		})
	}
	if class, ok := AccessOf("test.node.owner.undeclared"); ok || class != AccessUnmarked {
		t.Fatalf("undeclared AccessOf = (%d, %t), want (0, false)", class, ok)
	}
}

func ownerNodeRefusalCallers() map[string]context.Context {
	callers := map[string]context.Context{
		"anonymous":                context.Background(),
		"owner":                    principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}),
		"owner-without-cli-marker": principal.WithCaller(context.Background(), principal.Caller{}),
	}
	for _, kind := range []principal.Kind{
		principal.KindSession, principal.KindMCP, principal.KindHook,
		principal.KindNodeJob, principal.KindRemoteClient, principal.KindPlugin,
	} {
		p := principal.Principal{ID: "test-caller", Kind: kind, Scope: "project:test"}
		callers[string(kind)] = principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
	}
	return callers
}

func assertOwnerNodeRefusal(ctx context.Context, t *testing.T, r *Registry, method string, want error, kind cascade.Kind, code int, msg string) {
	t.Helper()
	result, wire := r.Dispatch(ctx, &Request{Method: method})
	if result != nil || wire == nil || wire.Code != code || wire.Message != msg {
		t.Fatalf("Dispatch(%q) = (%v, %v), want code %d and message %q", method, result, wire, code, msg)
	}
	if data, ok := wire.Data.(map[string]string); !ok || data["kind"] != kind.String() {
		t.Fatalf("Dispatch(%q) error data = %v, want kind %s", method, wire.Data, kind)
	}
	err := checkAccess(ctx, declaredAccess(method))
	if err != want {
		t.Fatalf("checkAccess(%q) = %v, want canonical error %v", method, err, want)
	}
	ce, ok := err.(*cascade.Error)
	if !ok || ce.Kind != kind || ce.Msg != msg || ce.Error() != kind.String()+": "+msg {
		t.Fatalf("checkAccess(%q) = %v, want kind %s and message %q", method, err, kind, msg)
	}
}
