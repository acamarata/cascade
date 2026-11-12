package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = DeclareAccess("test.node.dispatch", AccessNode)

// TestDispatchRefusesAccessNode verifies policy-enabled registries deny node methods.
func TestDispatchRefusesAccessNode(t *testing.T) {
	for _, resolve := range []func(string) (principal.Principal, error){
		nil,
		func(string) (principal.Principal, error) {
			t.Fatal("node refusal reached principal resolver")
			return principal.Principal{}, nil
		},
	} {
		r := NewRegistry()
		r.SetAccessPolicy(resolve)
		r.Register("test.node.dispatch", func(context.Context, json.RawMessage) (any, error) {
			t.Error("node refusal reached handler")
			return nil, nil
		})
		r.Use(func(_ string, next HandlerFunc) HandlerFunc {
			t.Error("node refusal reached middleware")
			return next
		})
		ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
		denied := checkAccess(ctx, declaredAccess("test.node.dispatch"))
		if denied != errAccessDenied || denied.(*cascade.Error).Msg != "caller not permitted" {
			t.Fatalf("access refusal = %v, want canonical permission denial", denied)
		}
		result, err := r.Dispatch(ctx, &Request{Method: "test.node.dispatch"})
		if result != nil || err == nil || err.Code != cascade.RPCCodePermissionDenied || err.Message != "caller not permitted" {
			t.Fatalf("dispatch = (%v, %v), want permission denial", result, err)
		}
		if data, ok := err.Data.(map[string]string); !ok || data["kind"] != cascade.KindPermissionDenied.String() {
			t.Fatalf("dispatch error data = %v, want permission-denied kind", err.Data)
		}
	}
}
