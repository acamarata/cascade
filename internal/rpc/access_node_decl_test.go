package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = DeclareAccess("test.node.declared", AccessNode)

func TestAccessNodeDeclared(t *testing.T) {
	if AccessNode <= AccessIngest {
		t.Fatalf("AccessNode = %d, want greater than AccessIngest = %d", AccessNode, AccessIngest)
	}
	if class, ok := AccessOf("test.node.declared"); !ok || class != AccessNode {
		t.Fatalf("declared method: AccessOf = (%d, %t), want (%d, true)", class, ok, AccessNode)
	}
	if class, ok := AccessOf("test.node.undeclared"); ok || class != 0 {
		t.Fatalf("undeclared method: AccessOf = (%d, %t), want (0, false)", class, ok)
	}
}

func TestAccessNodeCheckExhaustive(t *testing.T) {
	r := NewRegistry()
	r.SetAccessPolicy(nil)
	calls, middleware := 0, 0
	r.Register("test.node.declared", func(context.Context, json.RawMessage) (any, error) {
		calls++
		return nil, nil
	})
	r.Use(func(_ string, next HandlerFunc) HandlerFunc { middleware++; return next })
	contexts := []context.Context{
		context.Background(),
		principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}),
	}
	for _, kind := range []principal.Kind{principal.KindSession, principal.KindMCP, principal.KindHook, principal.KindNodeJob, principal.KindRemoteClient} {
		p := principal.Principal{ID: "test", Kind: kind, Scope: "project:test"}
		contexts = append(contexts, principal.WithCaller(context.Background(), principal.Caller{Principal: &p}))
	}
	for _, ctx := range contexts {
		_, err := r.Dispatch(ctx, &Request{Method: "test.node.declared"})
		if err == nil || err.Code != cascade.RPCCodePermissionDenied || err.Message != "caller not permitted" {
			t.Fatalf("AccessNode dispatch: %v, want permission denied", err)
		}
		if data, ok := err.Data.(map[string]string); !ok || data["kind"] != cascade.KindPermissionDenied.String() {
			t.Fatalf("AccessNode error data = %v, want permission-denied kind", err.Data)
		}
	}
	if calls != 0 || middleware != 0 {
		t.Fatalf("refused dispatch reached handler %d times and middleware %d times", calls, middleware)
	}
}
