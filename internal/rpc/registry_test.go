package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRegistry_RoundTrip(t *testing.T) {
	reg := NewRegistry()
	reg.Register("echo", func(_ context.Context, params json.RawMessage) (any, error) {
		return map[string]string{"got": string(params)}, nil
	})

	req, errObj := Parse([]byte(`{"jsonrpc":"2.0","method":"echo","params":{"a":1},"id":1}`))
	if errObj != nil {
		t.Fatalf("Parse: %+v", errObj)
	}
	result, dispatchErr := reg.Dispatch(context.Background(), req)
	if dispatchErr != nil {
		t.Fatalf("Dispatch: %+v", dispatchErr)
	}
	m, ok := result.(map[string]string)
	if !ok || m["got"] != `{"a":1}` {
		t.Errorf("result = %#v, want echoed params", result)
	}
}

func TestRegistry_UnknownMethod(t *testing.T) {
	reg := NewRegistry()
	req, _ := Parse([]byte(`{"jsonrpc":"2.0","method":"nope","id":1}`))
	_, errObj := reg.Dispatch(context.Background(), req)
	if errObj == nil {
		t.Fatal("expected an error for unknown method")
	}
	if errObj.Code != codeMethodNotFound {
		t.Errorf("Code = %d, want %d (-32601)", errObj.Code, codeMethodNotFound)
	}
	data, ok := errObj.Data.(map[string]string)
	if !ok || data["kind"] != cascade.KindNotFound.String() {
		t.Errorf("Data = %#v, want kind=not-found", errObj.Data)
	}
}

func TestRegistry_MiddlewareOrderAndChaining(t *testing.T) {
	reg := NewRegistry()
	var order []string
	reg.Use(func(_ string, next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, params json.RawMessage) (any, error) {
			order = append(order, "first-before")
			r, err := next(ctx, params)
			order = append(order, "first-after")
			return r, err
		}
	})
	reg.Use(func(_ string, next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, params json.RawMessage) (any, error) {
			order = append(order, "second-before")
			r, err := next(ctx, params)
			order = append(order, "second-after")
			return r, err
		}
	})
	reg.Register("m", func(_ context.Context, _ json.RawMessage) (any, error) {
		order = append(order, "handler")
		return "ok", nil
	})

	req, _ := Parse([]byte(`{"jsonrpc":"2.0","method":"m","id":1}`))
	if _, errObj := reg.Dispatch(context.Background(), req); errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}

	want := []string{"first-before", "second-before", "handler", "second-after", "first-after"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestRegistry_HandlerErrorWiresTaxonomyCode(t *testing.T) {
	reg := NewRegistry()
	reg.Register("fail", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, cascade.New(cascade.KindConflict, "boom")
	})
	req, _ := Parse([]byte(`{"jsonrpc":"2.0","method":"fail","id":1}`))
	_, errObj := reg.Dispatch(context.Background(), req)
	if errObj == nil || errObj.Code != cascade.RPCCodeConflict {
		t.Fatalf("errObj = %+v, want RPCCodeConflict (%d)", errObj, cascade.RPCCodeConflict)
	}
}

func TestRegistry_HandlerErrorNonTaxonomyFallsBackToInternal(t *testing.T) {
	reg := NewRegistry()
	reg.Register("fail", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, errors.New("raw error")
	})
	req, _ := Parse([]byte(`{"jsonrpc":"2.0","method":"fail","id":1}`))
	_, errObj := reg.Dispatch(context.Background(), req)
	if errObj == nil || errObj.Code != cascade.RPCCodeInternal {
		t.Fatalf("errObj = %+v, want RPCCodeInternal (%d)", errObj, cascade.RPCCodeInternal)
	}
}

func TestRegistry_Registered(t *testing.T) {
	reg := NewRegistry()
	if reg.Registered("m") {
		t.Fatal("Registered(m) should be false before Register")
	}
	reg.Register("m", func(_ context.Context, _ json.RawMessage) (any, error) { return nil, nil })
	if !reg.Registered("m") {
		t.Fatal("Registered(m) should be true after Register")
	}
}
